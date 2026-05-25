package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/zeebo/clingy"
)

//go:embed skill.md
var skillText string

type cmdSkill struct{}

func (c *cmdSkill) Setup(params clingy.Parameters) {}

func (c *cmdSkill) Execute(ctx context.Context) error {
	fmt.Fprint(clingy.Stdout(ctx), skillText)
	return nil
}
