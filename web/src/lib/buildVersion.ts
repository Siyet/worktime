// This value is baked into the JavaScript bundle. It deliberately does not come
// from a runtime API request, because that request can race an automatic restart.
export const CLIENT_BUILD_VERSION = __WORKTIME_BUILD_VERSION__;
