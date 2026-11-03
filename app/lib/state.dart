import 'dart:async';

import 'package:flutter/foundation.dart';

import 'api/api.dart';
import 'api/launcher.dart';
import 'api/models.dart';

/// An incoming chat message, for notifications.
class IncomingChat {
  final String peerName;
  final ChatMessage message;
  IncomingChat(this.peerName, this.message);
}

/// App-wide state, kept in sync with the core via its event stream.
class AppModel extends ChangeNotifier {
  CoreApi? api;
  String? fatalError;
  bool connected = false;

  Self? self;
  Settings? settings;
  List<Peer> peers = [];
  final Map<String, Offer> offers = {};
  final Map<String, Transfer> transfers = {};
  final Map<String, ChatSummary> chats = {};

  /// Loaded conversation threads (peer ID → messages, oldest first).
  final Map<String, List<ChatMessage>> threads = {};

  /// The chat currently on screen; its messages are marked read on arrival.
  String? openChat;

  /// New incoming offers (the UI pops a dialog for each).
  final _offerCtrl = StreamController<Offer>.broadcast();
  Stream<Offer> get newOffers => _offerCtrl.stream;
  final _closedCtrl = StreamController<String>.broadcast();
  Stream<String> get closedOffers => _closedCtrl.stream;
  final _chatCtrl = StreamController<IncomingChat>.broadcast();
  Stream<IncomingChat> get incomingChats => _chatCtrl.stream;

  bool _disposed = false;

  Future<void> start() async {
    try {
      api = await CoreLauncher.connect();
    } catch (e) {
      fatalError = e.toString();
      notifyListeners();
      return;
    }
    _eventLoop();
  }

  /// File transfers (text-only ones live in the chats).
  List<Transfer> get transferList =>
      transfers.values.where((t) => t.fileCount > 0).toList()..sort((a, b) => b.started.compareTo(a.started));

  bool isOnline(String peerId) => peers.any((p) => p.id == peerId);

  /// Conversations with devices that aren't currently visible.
  List<ChatSummary> get offlineChats =>
      chats.values.where((c) => !isOnline(c.peerId)).toList()
        ..sort((a, b) => (b.last?.time ?? DateTime(0)).compareTo(a.last?.time ?? DateTime(0)));

  Future<void> loadThread(String peerId) async {
    final ms = await api!.messages(peerId);
    threads[peerId] = ms;
    notifyListeners();
  }

  Future<void> markRead(String peerId) async {
    final c = chats[peerId];
    if (c != null && c.unread > 0) {
      chats[peerId] = c.copyWith(unread: 0);
      notifyListeners();
    }
    await api?.markRead(peerId).catchError((_) {});
  }

  Future<void> _sync() async {
    final s = await api!.state();
    self = Self.fromJson(s['self']);
    settings = Settings.fromJson(s['settings']);
    peers = [for (final p in (s['peers'] ?? const [])) Peer.fromJson(p)];
    final newOffers = {for (final o in (s['offers'] ?? const [])) Offer.fromJson(o)};
    for (final o in newOffers) {
      if (!offers.containsKey(o.id)) _offerCtrl.add(o);
    }
    offers
      ..clear()
      ..addEntries(newOffers.map((o) => MapEntry(o.id, o)));
    transfers
      ..clear()
      ..addEntries([for (final t in (s['transfers'] ?? const [])) Transfer.fromJson(t)].map((t) => MapEntry(t.id, t)));
    chats
      ..clear()
      ..addEntries([for (final c in (s['chats'] ?? const [])) ChatSummary.fromJson(c)].map((c) => MapEntry(c.peerId, c)));
    for (final id in threads.keys.toList()) {
      threads[id] = await api!.messages(id);
    }
  }

  void _applyChat(Map<String, dynamic> d) {
    final m = ChatMessage.fromJson(d['message']);
    final peerId = d['peerId'] as String;
    var isNew = true;
    final thread = threads[peerId];
    if (thread != null) {
      final i = thread.indexWhere((x) => x.id == m.id);
      if (i >= 0) {
        thread[i] = m;
        isNew = false;
      } else {
        thread.add(m);
      }
    } else if (chats[peerId]?.last?.id == m.id) {
      isNew = false;
    }
    final prev = chats[peerId];
    final newest = prev?.last == null || !m.time.isBefore(prev!.last!.time) || prev.last!.id == m.id;
    chats[peerId] = ChatSummary(
      peerId: peerId,
      peerName: d['peerName'] ?? prev?.peerName ?? '?',
      unread: d['unread'] ?? 0,
      online: isOnline(peerId),
      trusted: prev?.trusted ?? true,
      last: newest ? m : prev.last,
    );
    if (isNew && !m.out) {
      if (openChat == peerId) {
        markRead(peerId);
      }
      _chatCtrl.add(IncomingChat(d['peerName'] ?? '?', m));
    }
  }

  /// Keeps the event stream open, reconnecting with backoff.
  Future<void> _eventLoop() async {
    var delay = const Duration(milliseconds: 500);
    while (!_disposed) {
      try {
        await for (final e in api!.events()) {
          if (e.type == 'hello') {
            await _sync();
            connected = true;
            delay = const Duration(milliseconds: 500);
            notifyListeners();
            continue;
          }
          _apply(e);
          notifyListeners();
        }
      } catch (_) {}
      connected = false;
      notifyListeners();
      await Future.delayed(delay);
      delay = delay * 2 > const Duration(seconds: 5) ? const Duration(seconds: 5) : delay * 2;
      // The desktop daemon may have been restarted: find it again.
      try {
        api = await CoreLauncher.connect();
      } catch (_) {}
    }
  }

  void _apply(CoreEvent e) {
    switch (e.type) {
      case 'peers':
        peers = [for (final p in (e.data ?? const [])) Peer.fromJson(p)];
      case 'offer':
        final o = Offer.fromJson(e.data);
        offers[o.id] = o;
        _offerCtrl.add(o);
      case 'offer_closed':
        offers.remove(e.data['id']);
        _closedCtrl.add(e.data['id']);
      case 'transfer':
        final t = Transfer.fromJson(e.data);
        transfers[t.id] = t;
      case 'chat':
        _applyChat(e.data);
      case 'chat_read':
        final c = chats[e.data['peerId']];
        if (c != null) chats[c.peerId] = c.copyWith(unread: 0);
      case 'chat_deleted':
        chats.remove(e.data['peerId']);
        threads.remove(e.data['peerId']);
      case 'settings':
        settings = Settings.fromJson(e.data);
        if (self != null) {
          self = Self.fromJson({'id': self!.id, 'name': settings!.name, 'os': self!.os, 'port': self!.port});
        }
      case 'cleared':
        transfers.removeWhere((_, t) => !t.active);
    }
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }
}
