import 'package:flutter/material.dart';

import '../api/models.dart';

IconData osIcon(String os) => switch (os) {
      'android' => Icons.phone_android,
      'windows' => Icons.desktop_windows_outlined,
      'darwin' => Icons.laptop_mac,
      _ => Icons.laptop,
    };

class DeviceTile extends StatelessWidget {
  final Peer peer;
  final ChatSummary? chat;
  final VoidCallback onTap;
  final VoidCallback? onLongPress;

  /// Non-null puts the tile in selection mode with a checkbox.
  final bool? selected;
  final ValueChanged<bool>? onSelect;
  const DeviceTile(
      {super.key, required this.peer, required this.onTap, this.chat, this.onLongPress, this.selected, this.onSelect});

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card(
      elevation: 0,
      color: selected == true ? cs.secondaryContainer : cs.surfaceContainerHigh,
      child: ListTile(
        onTap: onTap,
        selected: selected == true,
        onLongPress: onLongPress,
        leading: CircleAvatar(
          backgroundColor: cs.secondaryContainer,
          child: Icon(osIcon(peer.os), color: cs.onSecondaryContainer),
        ),
        title: Text(peer.name, style: const TextStyle(fontWeight: FontWeight.w600)),
        subtitle: Text(
          chat?.last != null ? chat!.preview : (peer.trusted ? 'Paired · tap to chat or send files' : 'New device · tap to connect'),
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: chat != null && chat!.unread > 0 ? const TextStyle(fontWeight: FontWeight.w600) : null,
        ),
        trailing: selected != null
            ? Checkbox(value: selected, onChanged: (v) => onSelect?.call(v ?? false))
            : chat != null && chat!.unread > 0
            ? Badge(label: Text('${chat!.unread}'), largeSize: 22)
            : Icon(Icons.chat_bubble_outline, color: cs.outline),
      ),
    );
  }
}
