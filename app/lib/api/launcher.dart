import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;

import 'api.dart';

/// Finds or starts the Go core and returns a client for it.
///
/// * Linux: the core runs as a separate `homedropd daemon` process so it keeps
///   receiving after the window is closed. Its endpoint is in
///   ~/.config/homedrop/control.json.
/// * Android: the core is linked into the app (gomobile) and runs inside a
///   foreground service; we ask the service for the endpoint.
class CoreLauncher {
  static const _channel = MethodChannel('homedrop/core');

  static Future<CoreApi> connect() async {
    if (Platform.isAndroid) {
      final ep = jsonDecode(await _channel.invokeMethod<String>('start') ?? '{}');
      return CoreApi(ep['port'], ep['token']);
    }
    return _connectDesktop();
  }

  static String get _configDir {
    final xdg = Platform.environment['XDG_CONFIG_HOME'];
    final home = Platform.environment['HOME'] ?? '.';
    return '${xdg != null && xdg.isNotEmpty ? xdg : '$home/.config'}/homedrop';
  }

  static Future<CoreApi?> _existing() async {
    try {
      final f = File('$_configDir/control.json');
      if (!await f.exists()) return null;
      final ep = jsonDecode(await f.readAsString());
      final api = CoreApi(ep['port'], ep['token']);
      final r = await http.get(Uri.parse('http://127.0.0.1:${ep['port']}/api/state'),
          headers: {'Authorization': 'Bearer ${ep['token']}'}).timeout(const Duration(seconds: 1));
      return r.statusCode == 200 ? api : null;
    } catch (_) {
      return null;
    }
  }

  /// Where the bundled daemon lives: next to the app binary in a release
  /// bundle, or core/bin when running from the source tree.
  static String? _daemonPath() {
    final exeDir = File(Platform.resolvedExecutable).parent.path;
    final candidates = [
      '$exeDir/homedropd',
      '$exeDir/lib/homedropd',
      '${Directory.current.path}/../core/bin/homedropd',
    ];
    for (final c in candidates) {
      if (File(c).existsSync()) return c;
    }
    return null;
  }

  static Future<CoreApi> _connectDesktop() async {
    final running = await _existing();
    if (running != null) return running;

    final path = _daemonPath();
    if (path == null) {
      throw CoreError('homedropd not found. Build it with: cd core && go build -o bin/homedropd ./cmd/homedropd');
    }
    // Detached: the daemon outlives this window so you can still receive.
    await Process.start(path, ['daemon'], mode: ProcessStartMode.detached);
    for (var i = 0; i < 50; i++) {
      await Future.delayed(const Duration(milliseconds: 100));
      final api = await _existing();
      if (api != null) return api;
    }
    throw CoreError('HomeDrop service did not start (is port 53530 free?)');
  }
}
