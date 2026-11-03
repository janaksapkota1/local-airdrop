import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/models.dart';
import '../state.dart';

class TransferTile extends StatelessWidget {
  final Transfer transfer;
  final AppModel model;
  const TransferTile({super.key, required this.transfer, required this.model});

  String get _what {
    final t = transfer;
    if (t.fileCount == 0) return 'Text';
    if (t.fileCount == 1) return t.roots.first;
    return '${t.fileCount} files · ${t.roots.take(2).join(', ')}${t.roots.length > 2 ? '…' : ''}';
  }

  String get _status {
    final t = transfer;
    return switch (t.state) {
      TransferState.waiting => t.pairCode != null && !t.trusted
          ? 'Waiting for ${t.peerName} to accept · code ${t.pairCode}'
          : 'Waiting for ${t.peerName} to accept…',
      TransferState.running => '${humanBytes(t.done)} of ${humanBytes(t.total)} · ${humanBytes(t.speed)}/s',
      TransferState.done => t.total > 0
          ? '${humanBytes(t.total)}${t.speed > 0 ? ' · ${humanBytes(t.speed)}/s avg' : ''}'
          : 'Delivered',
      TransferState.declined => t.error?.contains('space') == true ? 'Receiver is out of space' : 'Declined',
      TransferState.cancelled => t.error ?? 'Cancelled',
      TransferState.failed => t.error ?? 'Failed',
    };
  }

  @override
  Widget build(BuildContext context) {
    final t = transfer;
    final cs = Theme.of(context).colorScheme;
    final failed = t.state == TransferState.failed || t.state == TransferState.declined || t.state == TransferState.cancelled;
    return Card(
      elevation: 0,
      color: cs.surfaceContainerLow,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 8, 12),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            Icon(t.isSend ? Icons.north_east : Icons.south_west,
                size: 18, color: failed ? cs.error : (t.state == TransferState.done ? Colors.green : cs.primary)),
            const SizedBox(width: 8),
            Expanded(
              child: Text('${t.isSend ? 'To' : 'From'} ${t.peerName}',
                  style: const TextStyle(fontWeight: FontWeight.w600)),
            ),
            ..._actions(context),
          ]),
          const SizedBox(height: 2),
          Text(_what, maxLines: 1, overflow: TextOverflow.ellipsis),
          if (t.text != null && t.text!.isNotEmpty && t.fileCount == 0)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: SelectableText(t.text!, maxLines: 4, style: TextStyle(color: cs.onSurfaceVariant)),
            ),
          if (t.state == TransferState.running) ...[
            const SizedBox(height: 8),
            LinearProgressIndicator(value: t.fraction, borderRadius: BorderRadius.circular(4)),
          ],
          const SizedBox(height: 6),
          Text(_status,
              style: Theme.of(context)
                  .textTheme
                  .bodySmall
                  ?.copyWith(color: failed ? cs.error : cs.onSurfaceVariant)),
        ]),
      ),
    );
  }

  List<Widget> _actions(BuildContext context) {
    final t = transfer;
    return [
      if (t.active)
        IconButton(
          tooltip: 'Cancel',
          icon: const Icon(Icons.close),
          onPressed: () => model.api?.cancel(t.id).catchError((_) {}),
        ),
      if (t.text != null && t.text!.isNotEmpty)
        IconButton(
          tooltip: 'Copy text',
          icon: const Icon(Icons.copy),
          onPressed: () => Clipboard.setData(ClipboardData(text: t.text!)),
        ),
      if (!t.isSend && t.state == TransferState.done && t.dir != null && t.fileCount > 0 && Platform.isLinux)
        IconButton(
          tooltip: 'Open folder',
          icon: const Icon(Icons.folder_open),
          onPressed: () => Process.run('xdg-open', [t.dir!]),
        ),
    ];
  }
}
