// SPDX-License-Identifier: Apache-2.0
import { mount } from 'svelte';
import App from './App.svelte';
import './app.css';

const target = document.getElementById('app');
if (!target) {
  throw new Error('mockulus ui: no #app element to mount into');
}

export default mount(App, { target });
