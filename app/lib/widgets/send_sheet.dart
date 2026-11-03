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

/// Bottom sheet: what do you want to send to [peer]?
Future<void> showSendSheet(BuildContext context, AppModel model, Peer peer, {String? addr}) {
  return showModalBottomSheet(
    context: context,
    showDragHandle: true,
    builder: (sheet) => SafeArea(
      child: Column(mainAxisSize: MainAxisSize.min, children: [
        ListTile(
          leading: Icon(osIcon(peer.os)),
          title: Text('Send to ${peer.name}', style: Theme.of(context).textTheme.titleMedium),
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
            if (paths.isNotEmpty && context.mounted) await _send(context, model, to: peer, addr: addr, paths: paths);
          },
        ),
        if (!Platform.isAndroid)
          ListTile(
            leading: const Icon(Icons.folder_outlined),
            title: const Text('Folder'),
            onTap: () async {
              Navigator.pop(sheet);
              final dir = await FilePicker.getDirectoryPath();
              if (dir != null && context.mounted) await _send(context, model, to: peer, addr: addr, paths: [dir]);
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
            await _send(context, model, to: peer, addr: addr, text: clip);
          },
        ),
        ListTile(
          leading: const Icon(Icons.short_text),
          title: const Text('Text or link'),
          onTap: () async {
            Navigator.pop(sheet);
            final text = await _askText(context);
            if (text != null && text.isNotEmpty && context.mounted) {
              await _send(context, model, to: peer, addr: addr, text: text);
            }
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

/// For when discovery is blocked (guest Wi-Fi, firewall): send by IP:port.
Future<void> connectByAddress(BuildContext context, AppModel model) async {
  final ctrl = TextEditingController();
  final addr = await showDialog<String>(
    context: context,
    builder: (d) => AlertDialog(
      title: const Text('Connect by address'),
      content: TextField(
        controller: ctrl,
        autofocus: true,
        decoration: const InputDecoration(
          labelText: 'IP address',
          hintText: '192.168.1.20 or 192.168.1.20:53530',
          border: OutlineInputBorder(),
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(d), child: const Text('Cancel')),
        FilledButton(onPressed: () => Navigator.pop(d, ctrl.text.trim()), child: const Text('Next')),
      ],
    ),
  );
  if (addr == null || addr.isEmpty || !context.mounted) return;
  final full = addr.contains(':') ? addr : '$addr:53530';
  final peer = Peer.fromJson({'id': '', 'name': full, 'os': '', 'addr': full});
  await showSendSheet(context, model, peer, addr: full);
}

/// Content shared into HomeDrop from another app: choose a device.
Future<void> pickDeviceAndSend(BuildContext context, AppModel model, SharedContent shared) {
  return showModalBottomSheet(
    context: context,
    showDragHandle: true,
    builder: (sheet) => ListenableBuilder(
      listenable: model,
      builder: (_, _) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          ListTile(title: Text('Send ${shared.describe()} to…', style: Theme.of(context).textTheme.titleMedium)),
          if (model.peers.isEmpty)
            const Padding(padding: EdgeInsets.all(24), child: Text('Looking for devices…')),
          for (final p in model.peers)
            DeviceTile(
              peer: p,
              onTap: () {
                Navigator.pop(sheet);
                _send(context, model, to: p, paths: shared.paths, text: shared.text);
              },
            ),
          const SizedBox(height: 8),
        ]),
      ),
    ),
  );
}
