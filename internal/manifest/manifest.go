package manifest

type Source struct {
	Type         string `json:"type"`
	Endpoint     string `json:"endpoint,omitempty"`
	JqQuery      string `json:"jq_query,omitempty"`
	Repository   string `json:"repository,omitempty"`
	AssetPattern string `json:"asset_pattern,omitempty"`
}

type Manifest struct {
	AppID             string `json:"app_id"`
	Name              string `json:"name"`
	Comment           string `json:"comment"`
	DefaultInstallDir string `json:"default_install_dir"`
	BinaryPattern     string `json:"binary_pattern"`
	BinaryPath        string `json:"binary_path"`
	SymlinkAs         string `json:"symlink_as"`
	CliOnly           bool   `json:"cli_only"`
	Categories        string `json:"categories"`
	ExecFlags         string `json:"exec_flags"`
	DefaultUrlX64     string `json:"default_url_x64"`
	DefaultUrlArm64   string `json:"default_url_arm64"`
	Source            Source `json:"source"`
}
