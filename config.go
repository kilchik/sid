package main

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type Config interface {
	Params() interface{}
	Validate() error
}

func InitConfig(path string, c Config) error {
	if _, err := toml.DecodeFile(path, c.Params()); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}

	return nil
}

type configParams struct {
	BotName string `toml:"bot_name"`
	Token   string `toml:"token"`
	DBPath  string `toml:"db_path"`
}

type configImpl struct {
	params      configParams
	defaultPath string
}

func (c *configImpl) Params() interface{} {
	return &c.params
}

func (c *configImpl) Validate() error {
	logPrefix := "parsing config: "
	if len(c.params.BotName) == 0 {
		return fmt.Errorf(logPrefix + "bot_name is not set")
	}
	if len(c.params.Token) == 0 {
		return fmt.Errorf(logPrefix + "token is not set")
	}
	if len(c.params.DBPath) == 0 {
		return fmt.Errorf(logPrefix + "db_path is not set")
	}
	return nil
}
