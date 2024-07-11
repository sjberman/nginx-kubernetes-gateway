package config

const baseHTTPTemplateText = `
js_import /usr/lib/nginx/modules/njs/httpmatches.js;
{{- if .HTTP2 }}http2 on;{{ end }}
`
