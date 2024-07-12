package config

import (
	"path/filepath"
	gotemplate "text/template"

	"github.com/nginxinc/nginx-gateway-fabric/internal/framework/helpers"
	"github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/nginx/file"
	"github.com/nginxinc/nginx-gateway-fabric/internal/mode/static/state/dataplane"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate
//counterfeiter:generate . Generator

const (
	// configFolder is the folder where NGINX configuration files are stored.
	configFolder = "/etc/nginx"

	// httpFolder is the folder where NGINX HTTP configuration files are stored.
	httpFolder = configFolder + "/conf.d"

	// modulesIncludesFolder is the folder where the included "load_module" file is stored.
	modulesIncludesFolder = configFolder + "/module-includes"

	// secretsFolder is the folder where secrets (like TLS certs/keys) are stored.
	secretsFolder = configFolder + "/secrets"

	// includesFolder is the folder where are all include files are stored.
	includesFolder = configFolder + "/includes"

	// httpConfigFile is the path to the configuration file with HTTP configuration.
	httpConfigFile = httpFolder + "/http.conf"

	// configVersionFile is the path to the config version configuration file.
	configVersionFile = httpFolder + "/config-version.conf"

	// httpMatchVarsFile is the path to the http_match pairs configuration file.
	httpMatchVarsFile = httpFolder + "/matches.json"

	// loadModulesFile is the path to the file containing any load_module directives.
	loadModulesFile = modulesIncludesFolder + "/load-modules.conf"
)

// ConfigFolders is a list of folders where NGINX configuration files are stored.
var ConfigFolders = []string{httpFolder, secretsFolder, includesFolder, modulesIncludesFolder}

// Generator generates NGINX configuration files.
// This interface is used for testing purposes only.
type Generator interface {
	// Generate generates NGINX configuration files from internal representation.
	Generate(configuration dataplane.Configuration) []file.File
}

// GeneratorImpl is an implementation of Generator.
//
// It generates files to be written to the following locations, which must exist and available for writing:
// - httpFolder, for HTTP configuration files.
// - secretsFolder, for secrets.
//
// It also expects that the main NGINX configuration file nginx.conf is located in configFolder and nginx.conf
// includes (https://nginx.org/en/docs/ngx_core_module.html#include) the files from httpFolder.
type GeneratorImpl struct {
	plus bool
}

// NewGeneratorImpl creates a new GeneratorImpl.
func NewGeneratorImpl(plus bool) GeneratorImpl {
	return GeneratorImpl{plus: plus}
}

type executeResult struct {
	dest string
	data []byte
}

// executeFunc is a function that generates NGINX configuration from internal representation.
type executeFunc func(configuration dataplane.Configuration) []executeResult

// Generate generates NGINX configuration files from internal representation.
// It is the responsibility of the caller to validate the configuration before calling this function.
// In case of invalid configuration, NGINX will fail to reload or could be configured with malicious configuration.
// To validate, use the validators from the validation package.
func (g GeneratorImpl) Generate(conf dataplane.Configuration) []file.File {
	files := make([]file.File, 0, len(conf.SSLKeyPairs)+1 /* http config */)

	for id, pair := range conf.SSLKeyPairs {
		files = append(files, generatePEM(id, pair.Cert, pair.Key))
	}

	files = append(files, g.generateHTTPConfig(conf)...)

	files = append(files, generateConfigVersion(conf.Version))

	for id, bundle := range conf.CertBundles {
		files = append(files, generateCertBundle(id, bundle))
	}

	files = append(files, generateLoadModulesConf(conf))

	return files
}

func generatePEM(id dataplane.SSLKeyPairID, cert []byte, key []byte) file.File {
	c := make([]byte, 0, len(cert)+len(key)+1)
	c = append(c, cert...)
	c = append(c, '\n')
	c = append(c, key...)

	return file.File{
		Content: c,
		Path:    generatePEMFileName(id),
		Type:    file.TypeSecret,
	}
}

func generatePEMFileName(id dataplane.SSLKeyPairID) string {
	return filepath.Join(secretsFolder, string(id)+".pem")
}

func generateCertBundle(id dataplane.CertBundleID, cert []byte) file.File {
	return file.File{
		Content: cert,
		Path:    generateCertBundleFileName(id),
		Type:    file.TypeRegular,
	}
}

func generateCertBundleFileName(id dataplane.CertBundleID) string {
	return filepath.Join(secretsFolder, string(id)+".crt")
}

func (g GeneratorImpl) generateHTTPConfig(conf dataplane.Configuration) []file.File {
	fileBytes := make(map[string][]byte)

	for _, execute := range g.getExecuteFuncs() {
		results := execute(conf)
		for _, res := range results {
			fileBytes[res.dest] = append(fileBytes[res.dest], res.data...)
		}
	}

	files := make([]file.File, 0, len(fileBytes))
	for filepath, bytes := range fileBytes {
		files = append(files, file.File{
			Path:    filepath,
			Content: bytes,
			Type:    file.TypeRegular,
		})
	}

	return files
}

func (g GeneratorImpl) getExecuteFuncs() []executeFunc {
	return []executeFunc{
		executeBaseHTTPConfig,
		executeServers,
		g.executeUpstreams,
		executeSplitClients,
		executeMaps,
		executeTelemetry,
	}
}

// generateConfigVersion writes the config version file.
func generateConfigVersion(configVersion int) file.File {
	c := executeVersion(configVersion)

	return file.File{
		Content: c,
		Path:    configVersionFile,
		Type:    file.TypeRegular,
	}
}

const loadModuleTemplateText = `
{{- range $module := . }}
load_module {{ $module }};
{{- end }}
`

var loadModuleTemplate = gotemplate.Must(gotemplate.New("load module").Parse(loadModuleTemplateText))

func generateLoadModulesConf(conf dataplane.Configuration) file.File {
	loadModules := []string{
		"/usr/lib/nginx/modules/ngx_http_js_module.so",
	}

	if conf.Telemetry.Endpoint != "" {
		loadModules = append(loadModules, "modules/ngx_otel_module.so")
	}

	return file.File{
		Content: helpers.MustExecuteTemplate(loadModuleTemplate, loadModules),
		Path:    loadModulesFile,
		Type:    file.TypeRegular,
	}
}
