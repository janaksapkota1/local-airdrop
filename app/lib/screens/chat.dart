import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/models.dart';
import '../state.dart';
import '../widgets/device_tile.dart';

/// One-to-one chat with a device. Files sent either way appear inline.
class ChatScreen extends StatefulWidget {
  final AppModel model;
  final String peerId;
  final String peerName;
  final String peerOs;
  const ChatScreen({super.key, required this.model, required this.peerId, required this.peerName, this.peerOs = ''});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final _input = TextEditingController();
  final _focus = FocusNode();
  bool _sending = false;

  AppModel get model => widget.model;

  @override
  void initState() {
    super.initState();
    model.openChat = widget.peerId;
    model.loadThread(widget.peerId).catchError((_) {});
    model.markRead(widget.peerId);
  }

  @override
  void dispose() {
    if (model.openChat == widget.peerId) model.openChat = null;
    _input.dispose();
    _focus.dispose();
    super.dispose();
  }

  void _toast(String msg) => ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg)));

  Future<void> _sendText() async {
    final text = _input.text.trim();
    if (text.isEmpty || _sending) return;
    setState(() => _sending = true);
    try {
      await model.api!.sendChat(widget.peerId, text);
      _input.clear();
    } catch (e) {
      _toast('$e');
    } finally {
      if (mounted) setState(() => _sending = false);
      _focus.requestFocus();
    }
  }

  Future<void> _sendPaths(List<String> paths) async {
    if (paths.isEmpty) return;
    if (!model.isOnline(widget.peerId)) return _toast('${widget.peerName} is offline. Files can only be sent while it is online.');
    try {
      await model.api!.send(to: widget.peerId, paths: paths);
    } catch (e) {
      _toast('Could not send: $e');
    }
  }

  Future<void> _attach() async {
    final choice = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (s) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          ListTile(
            leading: const Icon(Icons.insert_drive_file_outlined),
            title: const Text('Files'),
            onTap: () => Navigator.pop(s, 'files'),
          ),
          if (!Platform.isAndroid)
            ListTile(
              leading: const Icon(Icons.folder_outlined),
              title: const Text('Folder'),
              onTap: () => Navigator.pop(s, 'folder'),
            ),
          ListTile(
            leading: const Icon(Icons.content_paste),
            title: const Text('Paste clipboard'),
            onTap: () => Navigator.pop(s, 'clip'),
          ),
          const SizedBox(height: 8),
        ]),
      ),
    );
    switch (choice) {
      case 'files':
        final files = await FilePicker.pickFiles();
        await _sendPaths(files.map((f) => f.path).whereType<String>().toList());
      case 'folder':
        final dir = await FilePicker.getDirectoryPath();
        if (dir != null) await _sendPaths([dir]);
      case 'clip':
        final clip = (await Clipboard.getData(Clipboard.kTextPlain))?.text ?? '';
        if (clip.isEmpty) return _toast('Clipboard is empty');
        _input.text = clip;
        _focus.requestFocus();
    }
  }

  Future<void> _deleteChat() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (d) => AlertDialog(
        title: const Text('Delete this chat?'),
        content: const Text('Messages are deleted on this device only. Received files are kept.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(d, false), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.pop(d, true), child: const Text('Delete')),
        ],
      ),
    );
    if (ok == true) {
      await model.api?.deleteChat(widget.peerId);
      if (mounted) Navigator.pop(context);
    }
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final online = model.isOnline(widget.peerId);
        final name = model.chats[widget.peerId]?.peerName ?? widget.peerName;
        final peer = model.peers.where((p) => p.id == widget.peerId).firstOrNull;
        final messages = model.threads[widget.peerId] ?? const <ChatMessage>[];
        final waiting = model.transfers.values
            .where((t) => t.peerId == widget.peerId && t.state == TransferState.waiting && t.pairCode != null && !t.trusted)
            .firstOrNull;
        return Scaffold(
          appBar: AppBar(
            titleSpacing: 0,
            title: Row(children: [
              CircleAvatar(radius: 18, child: Icon(osIcon(peer?.os ?? widget.peerOs), size: 20)),
              const SizedBox(width: 12),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(name, overflow: TextOverflow.ellipsis),
                  Text(online ? 'online' : 'offline',
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                          color: online ? Colors.green : Theme.of(context).colorScheme.outline)),
                ]),
              ),
            ]),
            actions: [
              PopupMenuButton<String>(
                onSelected: (v) => v == 'delete' ? _deleteChat() : null,
                itemBuilder: (_) => const [PopupMenuItem(value: 'delete', child: Text('Delete chat'))],
              ),
            ],
          ),
          body: Column(children: [
            if (peer != null && !peer.trusted)
              _Banner(
                icon: Icons.verified_user_outlined,
                text: waiting != null
                    ? 'Waiting for $name to accept. Check that it shows code ${waiting.pairCode}.'
                    : 'Your first message or file asks $name to accept and pair with this device.',
              ),
            Expanded(
              child: messages.isEmpty
                  ? Center(
                      child: Text('Say hi, or tap 📎 to send files',
                          style: TextStyle(color: Theme.of(context).colorScheme.outline)))
                  : ListView.builder(
                      reverse: true,
                      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                      itemCount: messages.length,
                      itemBuilder: (context, i) {
                        final m = messages[messages.length - 1 - i];
                        final prev = i + 1 < messages.length ? messages[messages.length - 2 - i] : null;
                        final showDay = prev == null || !_sameDay(prev.time, m.time);
                        return Column(children: [
                          if (showDay) _DayChip(m.time),
                          _Bubble(message: m, model: model),
                        ]);
                      },
                    ),
            ),
            _Composer(
              controller: _input,
              focus: _focus,
              sending: _sending,
              online: online,
              onSend: _sendText,
              onAttach: _attach,
            ),
          ]),
        );
      },
    );
  }
}

bool _sameDay(DateTime a, DateTime b) => a.year == b.year && a.month == b.month && a.day == b.day;

String _hhmm(DateTime t) => '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}';

class _DayChip extends StatelessWidget {
  final DateTime day;
  const _DayChip(this.day);

  @override
  Widget build(BuildContext context) {
    final now = DateTime.now();
    final label = _sameDay(day, now)
        ? 'Today'
        : _sameDay(day, now.subtract(const Duration(days: 1)))
            ? 'Yesterday'
            : '${day.day}/${day.month}/${day.year}';
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Chip(label: Text(label), visualDensity: VisualDensity.compact),
    );
  }
}

class _Banner extends StatelessWidget {
  final IconData icon;
  final String text;
  const _Banner({required this.icon, required this.text});

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Container(
      width: double.infinity,
      color: cs.tertiaryContainer,
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
      child: Row(children: [
        Icon(icon, size: 20, color: cs.onTertiaryContainer),
        const SizedBox(width: 12),
        Expanded(child: Text(text, style: TextStyle(color: cs.onTertiaryContainer))),
      ]),
    );
  }
}

class _Bubble extends StatelessWidget {
  final ChatMessage message;
  final AppModel model;
  const _Bubble({required this.message, required this.model});

  @override
  Widget build(BuildContext context) {
    final m = message;
    final cs = Theme.of(context).colorScheme;
    final bg = m.out ? cs.primaryContainer : cs.surfaceContainerHigh;
    final fg = m.out ? cs.onPrimaryContainer : cs.onSurface;
    return Align(
      alignment: m.out ? Alignment.centerRight : Alignment.centerLeft,
      child: ConstrainedBox(
        constraints: BoxConstraints(maxWidth: MediaQuery.sizeOf(context).width * 0.78),
        child: GestureDetector(
          onLongPress: m.text == null
              ? null
              : () {
                  Clipboard.setData(ClipboardData(text: m.text!));
                  ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Copied')));
                },
          child: Container(
            margin: const EdgeInsets.symmetric(vertical: 3),
            padding: const EdgeInsets.fromLTRB(12, 8, 12, 6),
            decoration: BoxDecoration(
              color: bg,
              borderRadius: BorderRadius.only(
                topLeft: const Radius.circular(16),
                topRight: const Radius.circular(16),
                bottomLeft: Radius.circular(m.out ? 16 : 4),
                bottomRight: Radius.circular(m.out ? 4 : 16),
              ),
            ),
            child: Column(crossAxisAlignment: CrossAxisAlignment.end, mainAxisSize: MainAxisSize.min, children: [
              if (m.isFiles)
                _FilesContent(message: m, model: model, color: fg)
              else
                Align(
                  alignment: Alignment.centerLeft,
                  child: SelectableText(m.text ?? '', style: TextStyle(color: fg, fontSize: 15)),
                ),
              const SizedBox(height: 2),
              Row(mainAxisSize: MainAxisSize.min, children: [
                Text(_hhmm(m.time), style: TextStyle(fontSize: 11, color: fg.withValues(alpha: 0.6))),
                if (m.out) ...[const SizedBox(width: 4), _StatusIcon(message: m, color: fg)],
              ]),
            ]),
          ),
        ),
      ),
    );
  }
}

class _StatusIcon extends StatelessWidget {
  final ChatMessage message;
  final Color color;
  const _StatusIcon({required this.message, required this.color});

  @override
  Widget build(BuildContext context) {
    final (icon, tip) = switch (message.status) {
      'sending' => (Icons.schedule, 'Sending…'),
      'queued' => (Icons.cloud_queue, 'Waiting for the device to come online'),
      'failed' => (Icons.error_outline, message.error ?? message.files?.error ?? 'Not delivered'),
      _ => (Icons.done, 'Delivered'),
    };
    return Tooltip(
      message: tip,
      child: Icon(icon, size: 14,
          color: message.status == 'failed' ? Theme.of(context).colorScheme.error : color.withValues(alpha: 0.7)),
    );
  }
}

class _FilesContent extends StatelessWidget {
  final ChatMessage message;
  final AppModel model;
  final Color color;
  const _FilesContent({required this.message, required this.model, required this.color});

  @override
  Widget build(BuildContext context) {
    final f = message.files!;
    // Live progress while the transfer is in memory; the summary otherwise.
    final t = message.transferId == null ? null : model.transfers[message.transferId];
    final state = t?.state.name ?? f.state;
    final title = f.fileCount == 1 ? f.roots.first : '${f.fileCount} files';
    final subtitle = f.fileCount > 1 ? f.roots.take(3).join(', ') + (f.roots.length > 3 ? '…' : '') : null;
    final status = switch (state) {
      'waiting' => 'Waiting for the other device to accept…',
      'running' when t != null => '${humanBytes(t.done)} of ${humanBytes(t.total)} · ${humanBytes(t.speed)}/s',
      'running' => 'Transferring…',
      'done' => humanBytes(f.total),
      'declined' => 'Declined',
      'cancelled' => 'Cancelled',
      _ => f.error ?? t?.error ?? 'Failed',
    };
    final single = f.fileCount == 1 && !f.roots.first.contains('/');
    final isFolder = f.roots.length == 1 && f.fileCount > 1;
    return SizedBox(
      width: 260,
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Icon(isFolder ? Icons.folder : (single ? Icons.insert_drive_file : Icons.folder_copy), color: color, size: 32),
          const SizedBox(width: 10),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(title, style: TextStyle(color: color, fontWeight: FontWeight.w600), maxLines: 2, overflow: TextOverflow.ellipsis),
              if (subtitle != null)
                Text(subtitle, style: TextStyle(color: color.withValues(alpha: 0.7), fontSize: 12), maxLines: 1, overflow: TextOverflow.ellipsis),
              Text(status, style: TextStyle(color: color.withValues(alpha: 0.75), fontSize: 12)),
            ]),
          ),
          if (t != null && t.active)
            IconButton(
              tooltip: 'Cancel',
              icon: Icon(Icons.close, color: color),
              onPressed: () => model.api?.cancel(t.id).catchError((_) {}),
            ),
          if (!message.out && state == 'done' && f.dir != null && Platform.isLinux)
            IconButton(
              tooltip: 'Open folder',
              icon: Icon(Icons.folder_open, color: color),
              onPressed: () => Process.run('xdg-open', [f.dir!]),
            ),
        ]),
        if (state == 'running' && t != null) ...[
          const SizedBox(height: 6),
          LinearProgressIndicator(value: t.fraction, borderRadius: BorderRadius.circular(4)),
        ],
      ]),
    );
  }
}

class _Composer extends StatelessWidget {
  final TextEditingController controller;
  final FocusNode focus;
  final bool sending, online;
  final VoidCallback onSend, onAttach;
  const _Composer({
    required this.controller,
    required this.focus,
    required this.sending,
    required this.online,
    required this.onSend,
    required this.onAttach,
  });

  @override
  Widget build(BuildContext context) {
    final desktop = !Platform.isAndroid;
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
        child: Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
          IconButton(tooltip: 'Send files', icon: const Icon(Icons.attach_file), onPressed: onAttach),
          Expanded(
            child: CallbackShortcuts(
              // Desktop: Enter sends, Shift+Enter makes a new line.
              bindings: {if (desktop) const SingleActivator(LogicalKeyboardKey.enter): onSend},
              child: TextField(
                controller: controller,
                focusNode: focus,
                autofocus: desktop,
                minLines: 1,
                maxLines: 6,
                textInputAction: desktop ? TextInputAction.none : TextInputAction.newline,
                decoration: InputDecoration(
                  hintText: online ? 'Message' : 'Message (sent when the device is back online)',
                  filled: true,
                  border: OutlineInputBorder(borderRadius: BorderRadius.circular(24), borderSide: BorderSide.none),
                  contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
                ),
              ),
            ),
          ),
          const SizedBox(width: 4),
          IconButton.filled(
            tooltip: 'Send',
            icon: sending
                ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2))
                : const Icon(Icons.send),
            onPressed: sending ? null : onSend,
          ),
        ]),
      ),
    );
  }
}
