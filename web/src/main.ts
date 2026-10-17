import './app.css';
import App from './App.svelte';
import { mount } from 'svelte';

const el = document.getElementById('app');
if (!el) throw new Error('#app missing');
mount(App, { target: el });
