package linux

type Info struct {
	Distro  string
	Version string
}

func Name() string { return "Linux" }

func GetInfo() Info {
	return Info{Distro: "Ubuntu", Version: "24.04"}
}
