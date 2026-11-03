// Data types mirroring the Go core's control API (core/internal/node).

class Self {
  final String id, name, os;
  final int port;
  Self.fromJson(Map<String, dynamic> j)
      : id = j['id'] ?? '',
        name = j['name'] ?? '',
        os = j['os'] ?? '',
        port = j['port'] ?? 0;
}

class Settings {
  final String name, downloadDir;
  final bool autoAccept;
  const Settings({required this.name, required this.downloadDir, required this.autoAccept});
  Settings.fromJson(Map<String, dynamic> j)
      : name = j['name'] ?? '',
        downloadDir = j['downloadDir'] ?? '',
        autoAccept = j['autoAccept'] ?? false;
  Map<String, dynamic> toJson() => {'name': name, 'downloadDir': downloadDir, 'autoAccept': autoAccept};
  Settings copyWith({String? name, String? downloadDir, bool? autoAccept}) => Settings(
        name: name ?? this.name,
        downloadDir: downloadDir ?? this.downloadDir,
        autoAccept: autoAccept ?? this.autoAccept,
      );
}

class Peer {
  final String id, name, os, addr;
  final bool trusted;
  Peer.fromJson(Map<String, dynamic> j)
      : id = j['id'],
        name = j['name'] ?? '?',
        os = j['os'] ?? '',
        addr = j['addr'] ?? '',
        trusted = j['trusted'] ?? false;
}

class Offer {
  final String id, senderId, senderName, senderOs, pairCode;
  final bool trusted;
  final int fileCount, totalSize;
  final List<String> roots;
  final String? text;
  final DateTime expiresAt;
  Offer.fromJson(Map<String, dynamic> j)
      : id = j['id'],
        senderId = j['senderId'] ?? '',
        senderName = j['senderName'] ?? '?',
        senderOs = j['senderOs'] ?? '',
        pairCode = j['pairCode'] ?? '',
        trusted = j['trusted'] ?? false,
        fileCount = j['fileCount'] ?? 0,
        totalSize = j['totalSize'] ?? 0,
        roots = List<String>.from(j['roots'] ?? const []),
        text = j['text'],
        expiresAt = DateTime.tryParse(j['expiresAt'] ?? '') ?? DateTime.now().add(const Duration(minutes: 2));
}

enum TransferState { waiting, running, done, failed, declined, cancelled }

class Transfer {
  final String id, direction, peerId, peerName;
  final String? pairCode, error, text, dir;
  final bool trusted;
  final int fileCount, total, done, speed;
  final List<String> roots;
  final TransferState state;
  final DateTime started;

  Transfer.fromJson(Map<String, dynamic> j)
      : id = j['id'],
        direction = j['direction'] ?? 'send',
        peerId = j['peerId'] ?? '',
        peerName = j['peerName'] ?? '?',
        pairCode = (j['pairCode'] as String?)?.isEmpty ?? true ? null : j['pairCode'],
        error = j['error'],
        text = j['text'],
        dir = j['dir'],
        trusted = j['trusted'] ?? false,
        fileCount = j['fileCount'] ?? 0,
        total = j['total'] ?? 0,
        done = j['done'] ?? 0,
        speed = j['speed'] ?? 0,
        roots = List<String>.from(j['roots'] ?? const []),
        state = TransferState.values.firstWhere((s) => s.name == j['state'], orElse: () => TransferState.failed),
        started = DateTime.tryParse(j['started'] ?? '') ?? DateTime.now();

  bool get isSend => direction == 'send';
  bool get active => state == TransferState.waiting || state == TransferState.running;
  double get fraction => total == 0 ? (state == TransferState.done ? 1 : 0) : done / total;
}

class CoreEvent {
  final String type;
  final dynamic data;
  CoreEvent(this.type, this.data);
}

String humanBytes(num n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var i = 0;
  var v = n.toDouble();
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i == 0 ? '${v.toInt()} B' : '${v.toStringAsFixed(v < 10 ? 1 : 0)} ${units[i]}';
}

class FilesSummary {
  final int fileCount, total;
  final List<String> roots;
  final String state;
  final String? dir, error;
  FilesSummary.fromJson(Map<String, dynamic> j)
      : fileCount = j['fileCount'] ?? 0,
        total = j['total'] ?? 0,
        roots = List<String>.from(j['roots'] ?? const []),
        state = j['state'] ?? '',
        dir = j['dir'],
        error = j['error'];
}

class ChatMessage {
  final String id, peerId, kind, status;
  final bool out;
  final String? text, transferId, error;
  final FilesSummary? files;
  final DateTime time;
  ChatMessage.fromJson(Map<String, dynamic> j)
      : id = j['id'],
        peerId = j['peerId'] ?? '',
        kind = j['kind'] ?? 'text',
        status = j['status'] ?? '',
        out = j['out'] ?? false,
        text = j['text'],
        transferId = j['transferId'],
        error = j['error'],
        files = j['files'] == null ? null : FilesSummary.fromJson(j['files']),
        time = DateTime.tryParse(j['time'] ?? '')?.toLocal() ?? DateTime.now();
  bool get isFiles => kind == 'files';
}

class ChatSummary {
  final String peerId, peerName;
  final int unread;
  final bool online, trusted;
  final ChatMessage? last;
  ChatSummary({required this.peerId, required this.peerName, required this.unread,
      required this.online, required this.trusted, this.last});
  ChatSummary.fromJson(Map<String, dynamic> j)
      : peerId = j['peerId'],
        peerName = j['peerName'] ?? '?',
        unread = j['unread'] ?? 0,
        online = j['online'] ?? false,
        trusted = j['trusted'] ?? false,
        last = j['last'] == null ? null : ChatMessage.fromJson(j['last']);
  ChatSummary copyWith({String? peerName, int? unread, ChatMessage? last}) => ChatSummary(
      peerId: peerId, peerName: peerName ?? this.peerName, unread: unread ?? this.unread,
      online: online, trusted: trusted, last: last ?? this.last);

  String get preview {
    final m = last;
    if (m == null) return '';
    if (m.isFiles) {
      final f = m.files!;
      final what = f.fileCount == 1 ? f.roots.first : '${f.fileCount} files';
      return '${m.out ? 'You sent' : 'Sent you'} $what';
    }
    return '${m.out ? 'You: ' : ''}${m.text ?? ''}';
  }
}
