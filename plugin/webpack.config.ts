import path from 'path';
import CopyWebpackPlugin from 'copy-webpack-plugin';
import type { Configuration } from 'webpack';

const config: Configuration = {
  context: path.resolve(__dirname, 'src'),
  entry: {
    module: './module.ts',
  },
  output: {
    path: path.resolve(__dirname, 'dist'),
    filename: '[name].js',
    library: {
      type: 'amd',
    },
    clean: {
      keep: /^gpx_noc_alert_manager/,
    },
  },
  externals: [
    'lodash',
    'jquery',
    'moment',
    'slate',
    'emotion',
    '@emotion/react',
    '@emotion/css',
    'prismjs',
    'slate-plain-serializer',
    '@grafana/slate-react',
    'react',
    'react-dom',
    'react/jsx-runtime',
    'react/jsx-dev-runtime',
    'react-redux',
    'redux',
    'rxjs',
    'react-router',
    'react-router-dom',
    'd3',
    'angular',
    '@grafana/ui',
    '@grafana/runtime',
    '@grafana/data',
  ],
  resolve: {
    extensions: ['.ts', '.tsx', '.js', '.jsx'],
  },
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        use: {
          loader: 'ts-loader',
          options: {
            configFile: path.resolve(__dirname, 'tsconfig.json'),
          },
        },
        exclude: /node_modules/,
      },
      {
        test: /\.css$/,
        use: ['style-loader', 'css-loader'],
      },
    ],
  },
  plugins: [
    new CopyWebpackPlugin({
      patterns: [
        { from: 'plugin.json', to: '.' },
        { from: 'img', to: 'img', noErrorOnMissing: true },
      ],
    }),
  ],
  optimization: {
    splitChunks: false,
  },
  devtool: 'source-map',
};

export default config;
