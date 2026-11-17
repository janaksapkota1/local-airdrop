import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/models.dart';
import '../share.dart';
import '../state.dart';
import 'device_tile.dart';

void _toast(BuildContext context, String msg) =>
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg)));

Future<void> _send(BuildContext context, AppModel model,
    {Peer? to, String? addr, List<String> paths = const [], String? text}) async {
  try {
    await model.api!.send(to: to?.id, addr: addr, paths: paths, text: text);
  } catch (e) {
    if (context.mounted) _toast(context, 'Could not send: $e');
  }
}

/// Sends the same content to several devices; each gets its own transfer.
Future<void> _sendMany(BuildContext context, AppModel model, List<Peer> peers,
    {bool all = false, List<String> paths = const [], String? text}) async {
  try {
    final r = await model.api!.sendMany(to: [for (final p in peers) p.id], all: all, paths: paths, text: text);
    if (!context.mounted) return;
    if (r.errors.isEmpty) {
      _toast(context, 'Sending to ${r.ids.length} device${r.ids.length == 1 ? '' : 's'}');
    } else {
      final names = {for (final p in model.peers) p.id: p.name};
      final failed = r.errors.keys.map((id) => names[id] ?? id.substring(0, 8)).join(', ');
      _toast(context, 'Sending to ${r.ids.length}; could not reach $failed');
    }
  } catch (e) {
    if (context.mounted) _toast(context, 'Could not send: $e');
  }
}

/// Bottom sheet: what do you want to send to [peer]?
Future<void> showSendSheet(BuildContext context, AppModel model, Peer peer, {String? addr}) => _showSheet(
      context,
      icon: osIcon(peer.os),
      title: 'Send to ${peer.name}',
      send: ({paths = const [], text}) => _send(context, model, to: peer, addr: addr, paths: paths, text: text),
    );

/// Bottom sheet: send the same thing to several devices at once. With
/// [all], it goes to every device visible at the moment of sending.
Future<void> showSendManySheet(BuildContext context, AppModel model, List<Peer> peers, {bool all = false}) =>
    _showSheet(
      context,
      icon: all ? Icons.cell_tower : Icons.groups_outlined,
      title: all ? 'Send to everyone (${peers.length})' : 'Send to ${_names(peers)}',
      send: ({paths = const [], text}) => _sendMany(context, model, peers, all: all, paths: paths, text: text),
    );

String _names(List<Peer> peers) {
  if (peers.length <= 3) return peers.map((p) => p.name).join(', ');
  return '${peers.take(2).map((p) => p.name).join(', ')} +${peers.length - 2} more';
}

typedef _SendFn = Future<void> Function({List<String> paths, String? text});

Future<void> _showSheet(BuildContext context, {required IconData icon, required String title, required _SendFn send}) {
  return showModalBottomSheet(
    context: context,
    showDragHandle: true,
    builder: (sheet) => SafeArea(
      child: Column(mainAxisSize: MainAxisSize.min, children: [
        ListTile(
          leading: Icon(icon),
          title: Text(title, style: Theme.of(context).textTheme.titleMedium, maxLines: 2, overflow: TextOverflow.ellipsis),
        ),
        const Divider(height: 1),
        ListTile(
          leading: const Icon(Icons.insert_drive_file_outlined),
          title: const Text('Files'),
          onTap: () async {
            Navigator.pop(sheet);
            final files = await FilePicker.pickFiles();
            final paths = files.map((f) => f.path).whereType<String>().toList();
            if (files.isNotEmpty && paths.isEmpty && context.mounted) {
              return _toast(context, 'Could not read the selected files');
            }
            if (paths.isNotEmpty && context.mounted) await send(paths: paths);
          },
        ),
        if (!Platform.isAndroid)
          ListTile(
            leading: const Icon(Icons.folder_outlined),
            title: const Text('Folder'),
            onTap: () async {
              Navigator.pop(sheet);
              final dir = await FilePicker.getDirectoryPath();
              if (dir != null && context.mounted) await send(paths: [dir]);
            },
          ),
        ListTile(
          leading: const Icon(Icons.content_paste),
          title: const Text('Clipboard'),
          subtitle: const Text('Send what you last copied'),
          onTap: () async {
            Navigator.pop(sheet);
            final clip = (await Clipboard.getData(Clipboard.kTextPlain))?.text ?? '';
            if (!context.mounted) return;
            if (clip.isEmpty) return _toast(context, 'Clipboard is empty');
            await send(text: clip);
          },
        ),
        ListTile(
          leading: const Icon(Icons.short_text),
          title: const Text('Text or link'),
          onTap: () async {
            Navigator.pop(sheet);
            final text = await _askText(context);
            if (text != null && text.isNotEmpty && context.mounted) await send(text: text);
          },
        ),
        const SizedBox(height: 8),
      ]),
    ),
  );
}

Future<String?> _askText(BuildContext context) {
  final ctrl = TextEditingController();
  return showDialog<String>(
    context: context,
    builder: (d) => AlertDialog(
      title: const Text('Send text'),
      content: TextField(
        controller: ctrl,
        autofocus: true,
        minLines: 3,
        maxLines: 8,
        decoration: const InputDecoration(border: OutlineInputBorder(), hintText: 'Message or link'),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(d), child: const Text('Cancel')),
        FilledButton(onPressed: () => Navigator.pop(d, ctrl.text), child: const Text('Send')),
      ],
    ),
  );
}

/// For when discovery is blocked (separate VLANs, guest Wi-Fi, firewall):
/// connect by IP. The device is remembered and shows up as online from then
/// on, on this device and on the other one.
Future<void> connectByAddress(BuildContext context, AppModel model) async {
  final ctrl = TextEditingController();
  final addr = await showDialog<String>(
    context: context,
    builder: (d) => AlertDialog(
      title: const Text('Connect by address'),
      content: Column(mainAxisSize: MainAxisSize.min, children: [
        TextField(
          controller: ctrl,
          autofocus: true,
          keyboardType: TextInputType.url,
          decoration: const InputDecoration(
            labelText: 'IP address',
            hintText: '192.168.1.20 or 192.168.1.20:53530',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 12),
        const Text('The device will be remembered and listed as online whenever it can be reached.'),
      ]),
      actions: [
        TextButton(onPressed: () => Navigator.pop(d), child: const Text('Cancel')),
        FilledButton(onPressed: () => Navigator.pop(d, ctrl.text.trim()), child: const Text('Next')),
      ],
    ),
  );
  if (addr == null || addr.isEmpty || !context.mounted) return;
  final full = addr.contains(':') ? addr : '$addr:53530';
  final Peer peer;
  try {
    peer = await model.api!.addAddress(full);
  } catch (e) {
    if (context.mounted) _toast(context, '$e');
    return;
  }
  if (!context.mounted) return;
  _toast(context, 'Added ${peer.name}');
  await showSendSheet(context, model, peer, addr: peer.addr);
}

/// Content shared into HomeDrop from another app: choose one or more devices.
Future<void> pickDeviceAndSend(BuildContext context, AppModel model, SharedContent shared) {
  final selected = <String>{};
  return showModalBottomSheet(
    context: context,
    showDragHandle: true,
    isScrollControlled: true,
    builder: (sheet) => StatefulBuilder(
      builder: (_, setSheet) => ListenableBuilder(
        listenable: model,
        builder: (_, _) {
          selected.removeWhere((id) => !model.isOnline(id));
          final chosen = model.peers.where((p) => selected.contains(p.id)).toList();
          return SafeArea(
            child: SingleChildScrollView(
              child: Column(mainAxisSize: MainAxisSize.min, children: [
                ListTile(
                  title: Text('Send ${shared.describe()} to…', style: Theme.of(context).textTheme.titleMedium),
                  subtitle: model.peers.length > 1 ? const Text('Tap one device, or tick several') : null,
                ),
                if (model.peers.isEmpty)
                  const Padding(padding: EdgeInsets.all(24), child: Text('Looking for devices…')),
                for (final p in model.peers)
                  DeviceTile(
                    peer: p,
                    selected: model.peers.length > 1 ? selected.contains(p.id) : null,
                    onSelect: (v) => setSheet(() => v ? selected.add(p.id) : selected.remove(p.id)),
                    onTap: () {
                      if (selected.isNotEmpty) {
                        setSheet(() => selected.contains(p.id) ? selected.remove(p.id) : selected.add(p.id));
                        return;
                      }
                      Navigator.pop(sheet);
                      _send(context, model, to: p, paths: shared.paths, text: shared.text);
                    },
                  ),
                if (model.peers.length > 1)
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
                    child: Row(children: [
                      Expanded(
                        child: OutlinedButton.icon(
                          icon: const Icon(Icons.cell_tower),
                          label: const Text('Everyone'),
                          onPressed: () {
                            Navigator.pop(sheet);
                            _sendMany(context, model, model.peers, all: true, paths: shared.paths, text: shared.text);
                          },
                        ),
                      ),
                      const SizedBox(width: 12),
                      Expanded(
                        child: FilledButton.icon(
                          icon: const Icon(Icons.send),
                          label: Text(chosen.isEmpty ? 'Send' : 'Send to ${chosen.length}'),
                          onPressed: chosen.isEmpty
                              ? null
                              : () {
                                  Navigator.pop(sheet);
                                  _sendMany(context, model, chosen, paths: shared.paths, text: shared.text);
                                },
                        ),
                      ),
                    ]),
                  ),
                const SizedBox(height: 8),
              ]),
            ),
          );
        },
      ),
    ),
  );
}
