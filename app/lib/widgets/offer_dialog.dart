import 'package:flutter/material.dart';

import '../api/models.dart';
import 'device_tile.dart';

/// "Phone wants to send you 3 files" — pops up for every incoming offer.
class OfferDialog extends StatelessWidget {
  final Offer offer;
  const OfferDialog({super.key, required this.offer});

  String get _what {
    final parts = <String>[];
    if (offer.fileCount > 0) {
      final names = offer.roots.take(3).join(', ') + (offer.roots.length > 3 ? ', …' : '');
      parts.add(offer.fileCount == 1
          ? '$names (${humanBytes(offer.totalSize)})'
          : '${offer.fileCount} files, ${humanBytes(offer.totalSize)}\n$names');
    }
    if (offer.text != null && offer.text!.isNotEmpty) parts.add('a text message');
    return parts.join('\n+ ');
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return AlertDialog(
      icon: Icon(osIcon(offer.senderOs), size: 36),
      title: Text('${offer.senderName} wants to send you'),
      content: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text(_what, style: Theme.of(context).textTheme.bodyLarge),
        if (offer.text != null && offer.text!.isNotEmpty) ...[
          const SizedBox(height: 12),
          Container(
            width: double.infinity,
            padding: const EdgeInsets.all(10),
            decoration: BoxDecoration(color: cs.surfaceContainerHighest, borderRadius: BorderRadius.circular(8)),
            child: Text(offer.text!, maxLines: 6, overflow: TextOverflow.ellipsis),
          ),
        ],
        if (!offer.trusted) ...[
          const SizedBox(height: 16),
          Container(
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(color: cs.tertiaryContainer, borderRadius: BorderRadius.circular(8)),
            child: Row(children: [
              Icon(Icons.verified_user_outlined, color: cs.onTertiaryContainer),
              const SizedBox(width: 12),
              Expanded(
                child: Text.rich(
                  TextSpan(children: [
                    const TextSpan(text: 'New device. Check the sender shows code '),
                    TextSpan(
                        text: offer.pairCode,
                        style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 18, letterSpacing: 2)),
                  ]),
                  style: TextStyle(color: cs.onTertiaryContainer),
                ),
              ),
            ]),
          ),
        ],
      ]),
      actions: [
        TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Decline')),
        FilledButton(onPressed: () => Navigator.pop(context, true), child: const Text('Accept')),
      ],
    );
  }
}
