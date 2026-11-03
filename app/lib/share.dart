import 'dart:io';

import 'package:flutter/services.dart';

/// Files or text another Android app shared to HomeDrop.
class SharedContent {
  final List<String> paths;
  final String? text;
  SharedContent(this.paths, this.text);

  bool get isEmpty => paths.isEmpty && (text == null || text!.isEmpty);

  String describe() {
    if (paths.isNotEmpty) return paths.length == 1 ? paths.first.split('/').last : '${paths.length} files';
    return 'text';
  }

  static SharedContent? fromMap(Object? m) {
    if (m is! Map) return null;
    final c = SharedContent(List<String>.from(m['paths'] ?? const []), m['text'] as String?);
    return c.isEmpty ? null : c;
  }
}

/// Bridges Android's share sheet (ACTION_SEND) into Flutter.
class ShareReceiver {
  static const _channel = MethodChannel('homedrop/share');

  static void listen(void Function(SharedContent) onShare) {
    if (!Platform.isAndroid) return;
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'shared') {
        final c = SharedContent.fromMap(call.arguments);
        if (c != null) onShare(c);
      }
    });
    // Content that launched the app.
    _channel.invokeMethod('initial').then((m) {
      final c = SharedContent.fromMap(m);
      if (c != null) onShare(c);
    }).catchError((_) {});
  }
}
