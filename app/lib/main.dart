import 'package:flutter/material.dart';

import 'screens/home.dart';
import 'state.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  final model = AppModel()..start();
  runApp(HomeDropApp(model: model));
}

class HomeDropApp extends StatelessWidget {
  final AppModel model;
  const HomeDropApp({super.key, required this.model});

  @override
  Widget build(BuildContext context) {
    ThemeData theme(Brightness b) => ThemeData(
          colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF2F6FED), brightness: b),
          useMaterial3: true,
        );
    return MaterialApp(
      title: 'HomeDrop',
      debugShowCheckedModeBanner: false,
      theme: theme(Brightness.light),
      darkTheme: theme(Brightness.dark),
      home: HomeScreen(model: model),
    );
  }
}
