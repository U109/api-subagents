const release = require('./desktop/release.json');

module.exports = {
  appId: 'com.u109.api-subagents',
  productName: 'API Subagents',
  directories: {output: 'release'},
  files: ['dist-desktop/**/*', 'package.json', '!node_modules/**/{test,tests,__tests__}/**/*'],
  extraResources: [
    {from: '.desktop-resources/plugin', to: 'plugin'},
    {from: '.desktop-resources/runtime', to: 'runtime'},
    {from: '.desktop-resources/licenses', to: 'licenses'},
  ],
  asar: true,
  win: {target: [{target: 'nsis', arch: ['x64']}], icon: 'desktop/icon.ico'},
  nsis: {
    oneClick: true,
    perMachine: false,
    createDesktopShortcut: true,
    createStartMenuShortcut: true,
    deleteAppDataOnUninstall: false,
    artifactName: 'API-Subagents-Setup-${version}-${arch}.${ext}',
  },
  publish: [{provider: 'github', owner: release.owner, repo: release.repo, releaseType: 'draft'}],
};
