import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';

import '../api/models.dart';
import '../share.dart';
import '../state.dart';
import '../widgets/device_tile.dart';
import '../widgets/offer_dialog.dart';
import '../widgets/send_sheet.dart';
import '../widgets/transfer_tile.dart';
import 'chat.dart';
import 'settings.dart';

class HomeScreen extends StatefulWidget {
  final AppModel model;
  const HomeScreen({super.key, required this.model});

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  final _subs = <StreamSubscription>[];
  final _openDialogs = <String, NavigatorState>{};

  AppModel get model => widget.model;
  bool _focused = true;

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) => _focused = state == AppLifecycleState.resumed;

  void _openChat(String peerId, String name, {String os = ''}) {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => ChatScreen(model: model, peerId: peerId, peerName: name, peerOs: os)),
    );
  }

  void _onIncomingChat(IncomingChat c) {
    if (!mounted || model.openChat == c.message.peerId) return;
    final m = c.message;
    final body = m.isFiles
        ? (m.files!.fileCount == 1 ? 'Sent you ${m.files!.roots.first}' : 'Sent you ${m.files!.fileCount} files')
        : (m.text ?? '');
    // Desktop notification when the window isn't in front.
    if (Platform.isLinux && !_focused) {
      Process.run('notify-send', ['-a', 'HomeDrop', '-i', 'network-transmit-receive', c.peerName, body])
          .catchError((_) => ProcessResult(0, 1, '', ''));
    }
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(
      duration: const Duration(seconds: 6),
      content: Text('${c.peerName}: $body', maxLines: 2, overflow: TextOverflow.ellipsis),
      action: SnackBarAction(label: 'Open', onPressed: () => _openChat(m.peerId, c.peerName)),
    ));
  }

  @override
  void initState() {
    super.initState();
    _subs.add(model.newOffers.listen(_showOffer));
    _subs.add(model.closedOffers.listen((id) {
      // The sender gave up or it timed out: close the dialog if still open.
      final nav = _openDialogs.remove(id);
      if (nav != null && nav.canPop()) nav.pop();
    }));
    WidgetsBinding.instance.addObserver(this);
    _subs.add(model.incomingChats.listen(_onIncomingChat));
    // Files/text shared into HomeDrop from another Android app.
    ShareReceiver.listen((shared) {
      if (mounted) pickDeviceAndSend(context, model, shared);
    });
  }

  void _showOffer(Offer o) {
    if (!mounted) return;
    final nav = Navigator.of(context);
    _openDialogs[o.id] = nav;
    showDialog<bool>(
      context: context,
      barrierDismissible: false,
      builder: (_) => OfferDialog(offer: o),
    ).then((accept) {
      if (_openDialogs.remove(o.id) == null) return; // closed by the core
      model.api?.decide(o.id, accept ?? false).catchError((_) {});
    });
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    for (final s in _subs) {
      s.cancel();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        if (model.fatalError != null) return _ErrorView(message: model.fatalError!);
        if (model.self == null) {
          return const Scaffold(body: Center(child: CircularProgressIndicator()));
        }
        return Scaffold(
          appBar: AppBar(
            title: const Text('HomeDrop'),
            actions: [
              IconButton(
                tooltip: 'Connect by IP address',
                icon: const Icon(Icons.lan_outlined),
                onPressed: () => connectByAddress(context, model),
              ),
              IconButton(
                tooltip: 'Settings',
                icon: const Icon(Icons.settings_outlined),
                onPressed: () => Navigator.push(
                    context, MaterialPageRoute(builder: (_) => SettingsScreen(model: model))),
              ),
            ],
          ),
          body: RefreshIndicator(
            onRefresh: () async => model.api?.refresh(),
            child: ListView(
              padding: const EdgeInsets.fromLTRB(16, 4, 16, 24),
              children: [
                _VisibleBanner(model: model),
                const SizedBox(height: 16),
                _SectionHeader(
                  'Nearby devices',
                  trailing: IconButton(
                    tooltip: 'Scan again',
                    icon: const Icon(Icons.refresh),
                    onPressed: () => model.api?.refresh(),
                  ),
                ),
                if (model.peers.isEmpty) const _EmptyDevices(),
                for (final p in model.peers)
                  DeviceTile(
                    peer: p,
                    chat: model.chats[p.id],
                    onTap: () => _openChat(p.id, p.name, os: p.os),
                    onLongPress: () => showSendSheet(context, model, p),
                  ),
                if (model.offlineChats.isNotEmpty) ...[
                  const SizedBox(height: 16),
                  const _SectionHeader('Chats with offline devices'),
                  for (final c in model.offlineChats) _OfflineChatTile(chat: c, onTap: () => _openChat(c.peerId, c.peerName)),
                ],
                if (model.transfers.isNotEmpty) ...[
                  const SizedBox(height: 16),
                  _SectionHeader(
                    'Transfers',
                    trailing: TextButton(
                      onPressed: model.transferList.any((t) => !t.active) ? () => model.api?.clearFinished() : null,
                      child: const Text('Clear'),
                    ),
                  ),
                  for (final t in model.transferList) TransferTile(transfer: t, model: model),
                ],
              ],
            ),
          ),
        );
      },
    );
  }
}

class _VisibleBanner extends StatelessWidget {
  final AppModel model;
  const _VisibleBanner({required this.model});

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card(
      elevation: 0,
      color: cs.primaryContainer,
      child: ListTile(
        leading: Icon(model.connected ? Icons.wifi_tethering : Icons.wifi_tethering_off, color: cs.onPrimaryContainer),
        title: Text('Visible as “${model.self!.name}”',
            style: TextStyle(color: cs.onPrimaryContainer, fontWeight: FontWeight.w600)),
        subtitle: Text(
          model.connected
              ? 'Ready to receive · ID ${model.self!.id.substring(0, 8)}'
              : 'Reconnecting to the HomeDrop service…',
          style: TextStyle(color: cs.onPrimaryContainer),
        ),
      ),
    );
  }
}

class _OfflineChatTile extends StatelessWidget {
  final ChatSummary chat;
  final VoidCallback onTap;
  const _OfflineChatTile({required this.chat, required this.onTap});

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Card(
      elevation: 0,
      color: cs.surfaceContainerLow,
      child: ListTile(
        onTap: onTap,
        leading: CircleAvatar(backgroundColor: cs.surfaceContainerHighest, child: Icon(Icons.devices, color: cs.outline)),
        title: Text(chat.peerName),
        subtitle: Text(chat.preview, maxLines: 1, overflow: TextOverflow.ellipsis),
        trailing: chat.unread > 0 ? Badge(label: Text('${chat.unread}')) : null,
      ),
    );
  }
}

class _SectionHeader extends StatelessWidget {
  final String title;
  final Widget? trailing;
  const _SectionHeader(this.title, {this.trailing});

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(left: 4, bottom: 4),
        child: Row(children: [
          Text(title, style: Theme.of(context).textTheme.titleMedium),
          const Spacer(),
          ?trailing,
        ]),
      );
}

class _EmptyDevices extends StatelessWidget {
  const _EmptyDevices();

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 24, horizontal: 8),
        child: Column(children: [
          Icon(Icons.devices_other, size: 48, color: Theme.of(context).colorScheme.outline),
          const SizedBox(height: 8),
          const Text('Looking for devices…', textAlign: TextAlign.center),
          const SizedBox(height: 4),
          Text(
            'Open HomeDrop on your other device and make sure both are on the same Wi-Fi.',
            textAlign: TextAlign.center,
            style: TextStyle(color: Theme.of(context).colorScheme.outline),
          ),
        ]),
      );
}

class _ErrorView extends StatelessWidget {
  final String message;
  const _ErrorView({required this.message});

  @override
  Widget build(BuildContext context) => Scaffold(
        body: Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(mainAxisSize: MainAxisSize.min, children: [
              const Icon(Icons.error_outline, size: 48),
              const SizedBox(height: 12),
              Text('HomeDrop could not start', style: Theme.of(context).textTheme.titleLarge),
              const SizedBox(height: 8),
              SelectableText(message, textAlign: TextAlign.center),
              if (Platform.isLinux) ...[
                const SizedBox(height: 16),
                const Text('Tip: check `ufw allow 53530` and that no other HomeDrop is running.'),
              ],
            ]),
          ),
        ),
      );
}
