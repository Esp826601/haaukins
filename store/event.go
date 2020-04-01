// Copyright (c) 2018-2019 Aalborg University
// Use of this source code is governed by a GPLv3
// license that can be found in the LICENSE file.

package store

import (
	"errors"
	"fmt"
	"github.com/aau-network-security/haaukins"
	"github.com/dgrijalva/jwt-go"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v2"
)

const (
	ID_KEY       = "I"
	TEAMNAME_KEY = "TN"
)

var (
	TeamExistsErr       = errors.New("Team already exists")
	UnknownTeamErr      = errors.New("Unknown team")
	UnknownTokenErr     = errors.New("Unknown token")
	NoFrontendErr       = errors.New("lab requires at least one frontend")
	InvalidFlagValueErr = errors.New("Incorrect value for flag")
	UnknownChallengeErr = errors.New("Unknown challenge")
)

type RawEvent struct {
	Name			string
	Tag				string
	Available		int32
	Capacity		int32
	Exercises  		string
	Frontends 		string
	StartedAt		string
	FinishExpected 	string
}

type EventConfig struct {
	Name       string     `yaml:"name"`
	Tag        Tag        `yaml:"tag"`
	Available  int        `yaml:"available"`
	Capacity   int        `yaml:"capacity"`
	Lab        Lab        `yaml:"lab"`
	StartedAt  *time.Time `yaml:"started-at,omitempty"`
	FinishExpected  *time.Time `yaml:"finish-req,omitempty"`
	FinishedAt *time.Time `yaml:"finished-at,omitempty"`
}

type RawEventFile struct {
	EventConfig `yaml:",inline"`
	Teams       []Team `yaml:"teams,omitempty"`
}

func (e EventConfig) Validate() error {
	if e.Name == "" {
		return &EmptyVarErr{Var: "Name", Type: "Event"}
	}

	if e.Tag == "" {
		return &EmptyVarErr{Var: "Tag", Type: "Event"}
	}

	if len(e.Lab.Exercises) == 0 {
		return &EmptyVarErr{Var: "Exercises", Type: "Event"}
	}

	if len(e.Lab.Frontends) == 0 {
		return &EmptyVarErr{Var: "Frontends", Type: "Event"}
	}

	return nil
}

type Lab struct {
	Frontends []InstanceConfig `yaml:"frontends"`
	Exercises []Tag            `yaml:"exercises"`
}

type Challenge struct {
	OwnerID     string     `yaml:"-"`
	FlagTag     Tag        `yaml:"tag"`
	FlagValue   string     `yaml:"-"`
	CompletedAt *time.Time `yaml:"completed-at,omitempty"`
}

type Team struct {
	Id               string            `yaml:"id"`
	Email            string            `yaml:"email"`
	Name             string            `yaml:"name"`
	HashedPassword   string            `yaml:"hashed-password"`
	SolvedChallenges []Challenge       `yaml:"solved-challenges,omitempty"`
	Metadata         map[string]string `yaml:"metadata,omitempty"`
	CreatedAt        *time.Time        `yaml:"created-at,omitempty"`
	ChalMap          map[Tag]Challenge `yaml:"-"`
	AccessedAt       *time.Time        `yaml:"accessed-at,omitempty"`
}

func WithTeams(teams []*haaukins.Team) func (ts *teamstore){
	return func(ts *teamstore) {
		for _, t := range teams {
			ts.SaveTeam(t)
		}
	}
}

type EventConfigStore interface {
	Read() EventConfig
	SetCapacity(n int) error
	Finish(time.Time) error
}

type eventconfigstore struct {
	m     sync.Mutex
	conf  EventConfig
	hooks []func(EventConfig) error
}

func NewEventConfigStore(conf EventConfig, hooks ...func(EventConfig) error) *eventconfigstore {
	return &eventconfigstore{
		conf:  conf,
		hooks: hooks,
	}
}

func (es *eventconfigstore) Read() EventConfig {
	es.m.Lock()
	defer es.m.Unlock()

	return es.conf
}

func (es *eventconfigstore) SetCapacity(n int) error {
	es.m.Lock()
	defer es.m.Unlock()

	es.conf.Capacity = n

	return es.runHooks()
}

func (es *eventconfigstore) Finish(t time.Time) error {
	es.m.Lock()
	defer es.m.Unlock()

	es.conf.FinishedAt = &t

	return es.runHooks()
}

func (es *eventconfigstore) runHooks() error {
	for _, h := range es.hooks {
		if err := h(es.conf); err != nil {
			return err
		}
	}

	return nil
}

type EventFileHub interface {
	CreateEventFile(EventConfig) (EventFile, error)
}

type eventfilehub struct {
	m    sync.Mutex
	path string
}

type Archiver interface {
	ArchiveDir() string
	Archive() error
}

type EventFile interface {
	TeamStore
	EventConfigStore
	Archiver
}

type eventfile struct {
	m        sync.Mutex
	file     RawEventFile
	dir      string
	filename string

	TeamStore
	EventConfigStore
}

func NewEventFile(dir string, filename string, file RawEventFile) *eventfile {
	ef := &eventfile{
		dir:      dir,
		filename: filename,
		file:     file,
	}

	var teams []*haaukins.Team
	ts := NewTeamStore(WithTeams(teams), WithPostTeamHook(ef.saveTeams))
	for _, team  := range file.Teams {
		tn:= haaukins.NewTeam(team.Email, team.Name,"",team.Id,team.HashedPassword)
		teamtoken, err := GetTokenForTeam([]byte("testing purposes"), tn )
		if err != nil {
			log.Debug().Msgf("Error in getting token for team %s", tn.Name())
		}
		ts.tokens[teamtoken]=tn.ID()
		ts.emails[tn.Email()]=tn.ID()
		ts.teams[tn.ID()]=tn
		teams= append(teams, tn)
	}
	ef.TeamStore = ts
	ef.EventConfigStore = NewEventConfigStore(file.EventConfig, ef.saveEventConfig)

	return ef
}

func GetTokenForTeam(key []byte, t *haaukins.Team) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		ID_KEY:       t.ID(),
		TEAMNAME_KEY: t.Name(),
	})
	tokenStr, err := token.SignedString(key)
	if err != nil {
		return "", err
	}
	return tokenStr, nil
}

func (ef *eventfile) save() error {
	bytes, err := yaml.Marshal(ef.file)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(ef.path(), bytes, 0644)
}

func (ef *eventfile) delete() error {
	return os.Remove(ef.path())
}

func (ef *eventfile) saveTeams(teams []*haaukins.Team) error {
	ef.m.Lock()
	defer ef.m.Unlock()
	var storeTeam []Team
	for _, t := range teams {
		// solved challenges will be revisited it is NOT done !
		//for _, ch := range t.SolvedChallenges() {
		//	solvedChallenges=append(solvedChallenges, Challenge{FlagTag:Tag(ch.Tag)})
		//}
		now := time.Now()
		team := Team{
			Id:             t.ID(),
			Email:          t.Email(),
			Name:           t.Name(),
			HashedPassword: t.GetHashedPassword(),
			//SolvedChallenges: solvedChallenges,
			AccessedAt: &now,
		}
		storeTeam = append(storeTeam, team)
	}
	ef.file.Teams = storeTeam
	return ef.save()
}

func (ef *eventfile) saveEventConfig(conf EventConfig) error {
	ef.m.Lock()
	defer ef.m.Unlock()

	ef.file.EventConfig = conf

	return ef.save()
}

func (ef *eventfile) path() string {
	return filepath.Join(ef.dir, ef.filename)
}

func (ef *eventfile) ArchiveDir() string {
	parts := strings.Split(ef.filename, ".")
	relativeDir := strings.Join(parts[:len(parts)-1], ".")
	return filepath.Join(ef.dir, relativeDir)
}

func (ef *eventfile) Archive() error {
	ef.m.Lock()
	defer ef.m.Unlock()

	if _, err := os.Stat(ef.ArchiveDir()); os.IsNotExist(err) {
		if err := os.MkdirAll(ef.ArchiveDir(), os.ModePerm); err != nil {
			return err
		}
	}

	//cpy := eventfile{
	//	file:     ef.file,
	//	dir:      ef.ArchiveDir(),
	//	filename: "config.yml",
	//}
	//
	//cpy.file.Teams = []*haaukins.Team{}
	//for _, t := range ef.GetTeams() {
	//
	//	cpy.file.Teams = append(cpy.file.Teams, t)
	//}
	//cpy.save()

	if err := ef.delete(); err != nil {
		log.Warn().Msgf("Failed to delete old event file: %s", err)
	}

	return nil
}

func getFileNameForEvent(path string, tag Tag) (string, error) {
	now := time.Now().Format("02-01-06")
	dirname := fmt.Sprintf("%s-%s", tag, now)
	filename := fmt.Sprintf("%s.yml", dirname)

	_, dirErr := os.Stat(filepath.Join(path, dirname))
	_, fileErr := os.Stat(filepath.Join(path, filename))

	if os.IsNotExist(fileErr) && os.IsNotExist(dirErr) {
		return filename, nil
	}

	for i := 1; i < 999; i++ {
		dirname := fmt.Sprintf("%s-%s-%d", tag, now, i)
		filename := fmt.Sprintf("%s.yml", dirname)

		_, dirErr := os.Stat(filepath.Join(path, dirname))
		_, fileErr := os.Stat(filepath.Join(path, filename))

		if os.IsNotExist(fileErr) && os.IsNotExist(dirErr) {
			return filename, nil
		}
	}

	return "", fmt.Errorf("unable to get filename for event")
}
