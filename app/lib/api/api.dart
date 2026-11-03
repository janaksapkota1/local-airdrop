import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'models.dart';

/// Client for the Go core's localhost control API.
class CoreApi {
  final int port;
  final String token;
  final http.Client _http = http.Client();

  CoreApi(this.port, this.token);

  Uri _u(String path) => Uri.parse('http://127.0.0.1:$port$path');
  Map<String, String> get _h => {'Authorization': 'Bearer $token', 'Content-Type': 'application/json'};

  Future<dynamic> _req(String method, String path, [Object? body]) async {
    final req = http.Request(method, _u(path))..headers.addAll(_h);
    if (body != null) req.body = jsonEncode(body);
    final resp = await http.Response.fromStream(await _http.send(req).timeout(const Duration(seconds: 15)));
    final data = resp.body.isEmpty ? null : jsonDecode(resp.body);
    if (resp.statusCode != 200) {
      throw CoreError(data is Map ? (data['error'] ?? 'error ${resp.statusCode}') : 'error ${resp.statusCode}');
    }
    return data;
  }

  Future<Map<String, dynamic>> state() async => (await _req('GET', '/api/state')) as Map<String, dynamic>;

  Future<String> send({String? to, String? addr, List<String> paths = const [], String? text}) async {
    final r = await _req('POST', '/api/send', {
      'to': ?to,
      'addr': ?addr,
      'paths': paths,
      if (text != null && text.isNotEmpty) 'text': text,
    });
    return r['id'];
  }

  Future<void> decide(String offerId, bool accept) => _req('POST', '/api/offers/$offerId', {'accept': accept});
  Future<void> cancel(String transferId) => _req('POST', '/api/transfers/$transferId/cancel');
  Future<void> clearFinished() => _req('POST', '/api/transfers/clear');
  Future<void> refresh() => _req('POST', '/api/refresh');
  Future<Settings> updateSettings(Settings s) async =>
      Settings.fromJson(await _req('PUT', '/api/settings', s.toJson()));

  Future<List<ChatMessage>> messages(String peerId) async =>
      [for (final m in (await _req('GET', '/api/chats/$peerId')) as List) ChatMessage.fromJson(m)];
  Future<void> sendChat(String peerId, String text) =>
      _req('POST', '/api/chats/$peerId/messages', {'text': text});
  Future<void> markRead(String peerId) => _req('POST', '/api/chats/$peerId/read');
  Future<void> deleteChat(String peerId) => _req('DELETE', '/api/chats/$peerId');

  /// Server-Sent Events stream. Ends when the connection drops.
  Stream<CoreEvent> events() async* {
    final client = http.Client();
    try {
      final req = http.Request('GET', _u('/api/events'))..headers.addAll(_h);
      final resp = await client.send(req);
      if (resp.statusCode != 200) throw CoreError('events: HTTP ${resp.statusCode}');
      await for (final line in resp.stream.transform(utf8.decoder).transform(const LineSplitter())) {
        if (!line.startsWith('data: ')) continue;
        final j = jsonDecode(line.substring(6));
        yield CoreEvent(j['type'], j['data']);
      }
    } finally {
      client.close();
    }
  }
}

class CoreError implements Exception {
  final String message;
  CoreError(this.message);
  @override
  String toString() => message;
}
