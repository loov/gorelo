package windows

type Info struct {
	Edition string
	Build   int
}

func Name() string { return "Windows" }

func GetInfo() Info {
	return Info{Edition: "Pro", Build: 22631}
}
