// Mirrors the validation limits in internal/store/sync.go. The server is the authority -
// three writers reach it (this app, the MCP server, raw API tokens) - but a limit the
// client fails to enforce does not surface as a validation message: the row is written
// locally, the push 400s, and the row is quarantined on this device. So the numbers are
// duplicated deliberately, and internal/store/limits_test.go fails if they drift apart.
//
// All lengths count characters, matching the server's rune counting.
export const maxNameLength = 200;
export const maxTextLength = 2000;
export const maxTagsPerEntry = 8;
export const maxTagLength = 24;
