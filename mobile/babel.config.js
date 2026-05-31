// Standard Expo Babel config — also used by jest (babel-jest) to transform
// TypeScript test files. Matches Expo's default preset, so it doesn't change
// how Metro builds the app.
module.exports = function (api) {
  api.cache(true);
  return { presets: ['babel-preset-expo'] };
};
