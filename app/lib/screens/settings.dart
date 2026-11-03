import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/models.dart';
import '../state.dart';

class SettingsScreen extends StatefulWidget {
  final AppModel model;
  const SettingsScreen({super.key, required this.model});

  @override
  State<SettingsScreen> createState() => _SettingsScreenState();
}

class _SettingsScreenState extends State<SettingsScreen> {
  Future<void> _save(Settings s) async {
    try {
      await widget.model.api!.updateSettings(s);
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
    }
  }

  Future<void> _rename(Settings s) async {
    final ctrl = TextEditingController(text: s.name);
    final name = await showDialog<String>(
      context: context,
      builder: (d) => AlertDialog(
        title: const Text('Device name'),
        content: TextField(controller: ctrl, autofocus: true, decoration: const InputDecoration(border: OutlineInputBorder())),
        actions: [
          TextButton(onPressed: () => Navigator.pop(d), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.pop(d, ctrl.text.trim()), child: const Text('Save')),
        ],
      ),
    );
    if (name != null && name.isNotEmpty) await _save(s.copyWith(name: name));
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: widget.model,
      builder: (context, _) {
        final s = widget.model.settings;
        final self = widget.model.self;
        if (s == null || self == null) return const Scaffold(body: SizedBox());
        return Scaffold(
          appBar: AppBar(title: const Text('Settings')),
          body: ListView(children: [
            ListTile(
              leading: const Icon(Icons.badge_outlined),
              title: const Text('Device name'),
              subtitle: Text(s.name),
              onTap: () => _rename(s),
            ),
            ListTile(
              leading: const Icon(Icons.folder_outlined),
              title: const Text('Save received files to'),
              subtitle: Text(s.downloadDir),
              onTap: Platform.isAndroid
                  ? null
                  : () async {
                      final dir = await FilePicker.getDirectoryPath(initialDirectory: s.downloadDir);
                      if (dir != null) await _save(s.copyWith(downloadDir: dir));
                    },
            ),
            SwitchListTile(
              secondary: const Icon(Icons.bolt_outlined),
              title: const Text('Auto-accept from trusted devices'),
              subtitle: const Text('Skip the prompt for devices you have accepted before'),
              value: s.autoAccept,
              onChanged: (v) => _save(s.copyWith(autoAccept: v)),
            ),
            const Divider(),
            ListTile(
              leading: const Icon(Icons.fingerprint),
              title: const Text('Device ID'),
              subtitle: Text(self.id, style: const TextStyle(fontFamily: 'monospace', fontSize: 12)),
              onTap: () => Clipboard.setData(ClipboardData(text: self.id)),
            ),
            ListTile(
              leading: const Icon(Icons.lan_outlined),
              title: const Text('Port'),
              subtitle: Text('${self.port} (TCP + UDP 53530 must be allowed through the firewall)'),
            ),
          ]),
        );
      },
    );
  }
}
