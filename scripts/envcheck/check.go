package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// schemaState is what one database answers about the schema applied to it. The
// zero value means a database that is not there: present is false and the
// version is unknown.
type schemaState struct {
	name    string
	present bool
	version string
	dirty   bool
}

// noSchema is the version of a database that exists and carries no migration,
// which is the state a full reversal leaves behind.
const noSchema = "none"

// parseSchemaRow reads what psql prints for the schema version in its unaligned
// tuples-only form, which is "6|f" for version six applied cleanly. An empty
// answer is a database whose version table has no row.
func parseSchemaRow(name, raw string) (schemaState, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == noSchema {
		return schemaState{name: name, present: true, version: noSchema}, nil
	}
	fields := strings.Split(trimmed, "|")
	if len(fields) != 2 || fields[0] == "" {
		return schemaState{}, fmt.Errorf("database %s: schema version row is %q", name, trimmed)
	}
	return schemaState{name: name, present: true, version: fields[0], dirty: fields[1] == "t"}, nil
}

// compareSchema names every database that is missing, left dirty, or on a
// version the others are not.
func compareSchema(states []schemaState) []string {
	var out []string
	for _, each := range states {
		if !each.present {
			out = append(out, fmt.Sprintf("database %s does not exist", each.name))
			continue
		}
		if each.version == noSchema {
			out = append(out, fmt.Sprintf("database %s exists with no migration applied", each.name))
			continue
		}
		if each.dirty {
			out = append(out, fmt.Sprintf("database %s is at schema version %s and marked dirty", each.name, each.version))
		}
	}
	return append(out, disagreeingVersions(states)...)
}

// disagreeingVersions names the databases that are not on the same schema
// version. A migration applied to one and not the other fails much later, in a
// `relation does not exist` that never mentions a database.
func disagreeingVersions(states []schemaState) []string {
	applied := appliedOnly(states)
	if len(applied) < 2 || sameVersion(applied) {
		return nil
	}
	parts := make([]string, 0, len(applied))
	for _, each := range applied {
		parts = append(parts, each.name+" at "+each.version)
	}
	return []string{"the databases are on different schema versions: " + strings.Join(parts, ", ")}
}

func appliedOnly(states []schemaState) []schemaState {
	out := make([]schemaState, 0, len(states))
	for _, each := range states {
		if each.present && each.version != noSchema {
			out = append(out, each)
		}
	}
	return out
}

func sameVersion(states []schemaState) bool {
	for _, each := range states[1:] {
		if each.version != states[0].version {
			return false
		}
	}
	return true
}

// compareLifespan names a realm whose token lifespan is not the one its
// versioned file declares.
//
// An interrupted journey suite is the usual cause: the wallet suite shortens the
// lifespan to prove the border refuses an expired token and restores it at the
// end, so a run that died in the middle leaves every later token expiring at
// once, and every suite that asks for one gets a 401 that says nothing about the
// realm.
func compareLifespan(realm string, declared, observed int) []string {
	if declared == observed {
		return nil
	}
	return []string{fmt.Sprintf(
		"realm %s issues tokens for %ds, and its versioned file declares %ds",
		realm, observed, declared)}
}

// missingBroker names the queues or topics the versioned Terraform declares and
// the broker does not have.
func missingBroker(kind string, want, have []string) []string {
	var out []string
	for _, each := range want {
		if !named(have, each) {
			out = append(out, fmt.Sprintf("%s %s is declared in Terraform and the broker does not have it", kind, each))
		}
	}
	return out
}

// named answers whether the broker reported the declared name. A queue comes
// back as a URL and a topic as an ARN, and both end in the name Terraform gives.
func named(have []string, name string) bool {
	for _, each := range have {
		if each == name || strings.HasSuffix(each, "/"+name) || strings.HasSuffix(each, ":"+name) {
			return true
		}
	}
	return false
}

// staleImage names an image built before the commit the working tree is on.
//
// The image carries no commit of its own, so the build time against the commit
// time is the comparison available: one built before HEAD cannot contain it.
func staleImage(service string, built, commit time.Time) []string {
	if !built.Before(commit) {
		return nil
	}
	return []string{fmt.Sprintf(
		"the image %s runs was built at %s, before the commit at %s",
		service, stamp(built), stamp(commit))}
}

func stamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

var (
	resourceHead = regexp.MustCompile(`resource\s+"([^"]+)"\s+"[^"]+"\s*\{`)
	nameAttr     = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
)

// declaredNames reads the name of every resource of one type out of Terraform
// source, so the expected set of queues and topics stays in the files that
// provision them instead of being copied in here.
func declaredNames(resourceType, source string) []string {
	var out []string
	heads := resourceHead.FindAllStringSubmatchIndex(source, -1)
	for i, head := range heads {
		if source[head[2]:head[3]] != resourceType {
			continue
		}
		end := len(source)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		if found := nameAttr.FindStringSubmatch(source[head[1]:end]); found != nil {
			out = append(out, found[1])
		}
	}
	return out
}

// firstLine is the first line of what a command printed.
//
// A Compose service answers with one container id per replica, and the question
// asked of it is about the image they all share, so the first one answers for
// them. It also trims a failure to one line: a message broken over several lines
// is one finding read as many.
func firstLine(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		return strings.TrimSpace(trimmed[:i])
	}
	return trimmed
}
