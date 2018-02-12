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
	Token        string           `toml:"token"`
	DBPath       string           `toml:"db_path"`
	Leader       int64            `toml:"leader"`
	AllowedUsers map[string]int64 `toml:"allowed_users"`
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
	if len(c.params.Token) == 0 {
		return fmt.Errorf(logPrefix + "token is not set")
	}
	if len(c.params.DBPath) == 0 {
		return fmt.Errorf(logPrefix + "db_path is not set")
	}
	if c.params.Leader == 0 {
		return fmt.Errorf(logPrefix + "leader is not set")
	}
	if c.params.AllowedUsers == nil {
		return fmt.Errorf(logPrefix + "allowed_uids is not set")
	}
	return nil
}
