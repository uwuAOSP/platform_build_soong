package common

import "encoding/json"

type PrebuiltKernelModulesPropertiesJSON struct {
	// List or filegroup of prebuilt kernel module files. Should have .ko suffix.
	Srcs []string `json:",omitempty"`

	// List of kernel module filenames that will be loaded via modules.load.
	// Should have .ko suffix.
	// If empty, this will default to filenames of Srcs.
	// If no sources should be loaded, set `Load_by_default` to false instead.
	Src_filenames_to_load []string `json:",omitempty"`

	// List or filegroup of prebuilt kernel module files for 16k. Should have .ko suffix.
	// These files will be installed in lib/modules/16k-mode/
	// These files are ONLY loaded during the Second Boot Stage when the device is in 16k mode.
	Srcs_16k []string `json:",omitempty"`

	// List of system_dlkm prebuilt_kernel_modules that the local kernel modules
	// depend on. Use `:module_name{.modules.zip}` here.
	// The deps will be assembled into intermediates directory for running depmod
	// but will not be added to the current module's installed files.
	System_dep *string `json:",omitempty"`

	// If false, then srcs will not be included in modules.load.
	// This feature is used by system_dlkm
	Load_by_default *bool `json:",omitempty"`

	Blocklist_file *string `json:",omitempty"`

	// Path to the kernel module options file
	Options_file *string `json:",omitempty"`

	// Kernel version that these modules are for. Kernel modules are installed to
	// /lib/modules/<kernel_version> directory in the corresponding partition. Default is "".
	Kernel_version *string `json:",omitempty"`

	// Whether this module is directly installable to one of the partitions. Default is true
	Installable *bool `json:",omitempty"`

	// Whether debug symbols should be stripped from the *.ko files.
	// Defaults to true.
	Strip_debug_symbols *bool `json:",omitempty"`

	// Properties related to loading the kernel modules from a zip file.
	// This is useful if you want the list of kernel modules to be dynamic, and unknown at analysis
	// time, for example when supplying the kernel modules via CIPD.
	//
	// Most of these properties are mutually exclusive with the other, non-zip properties. But
	// some such as srcs will be merged with the contents / information from the zip file.
	Zip ZipProperties
}

type ZipProperties struct {
	// The zip file containing the kernel modules and other files like the load/blocklist files.
	Src *string `json:",omitempty"`

	// The name of the load file inside of the zip file. Only modules listed in it will
	// be installed.
	Load_file *string `json:",omitempty"`

	// List of extra kernel modules to add to the load file.
	Extra_loads []string `json:",omitempty"`

	// Optional file in the zip listing all modules to install. When omitted,
	// the load file remains the install list for backwards compatibility.
	Install_file *string `json:",omitempty"`

	// The name of the blocklist file inside of the zip file.
	Blocklist_file *string `json:",omitempty"`

	// Name of a .cfg file that's loaded by init.insmod.sh. This is just used
	// to determine the list of 16k kernel modules, taken from all the modprobe| lines
	// in the cfg file.
	Srcs_16k_cfg_file *string `json:",omitempty"`
}

func (p *PrebuiltKernelModulesPropertiesJSON) ToJSON() string {
	result, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(result)
}
