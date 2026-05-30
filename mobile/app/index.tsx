/**
 * Index route ("/"). The auth gate in _layout shows the login screen until a
 * host is connected, after which the tabs mount and this hidden route simply
 * forwards to the Terminal tab.
 */
import { Redirect } from 'expo-router';
import React from 'react';

export default function Index() {
  return <Redirect href="/terminal" />;
}
