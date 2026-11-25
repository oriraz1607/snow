import React from 'react';
import { AppPlugin, AppPluginMeta, PluginConfigPageProps } from '@grafana/data';
import { ConfigPage } from './pages/ConfigPage';
import { RootPage } from './pages/RootPage';
import { AppPluginSettings } from './types/api';

const PluginConfigBody: React.FC<PluginConfigPageProps<AppPluginMeta<AppPluginSettings>>> = (props) =>
  React.createElement(ConfigPage, { meta: props.plugin.meta });

export const plugin = new AppPlugin<AppPluginSettings>()
  .setRootPage(RootPage)
  .addConfigPage({
    title: 'Configuration',
    icon: 'cog',
    body: PluginConfigBody,
    id: 'config',
  });
