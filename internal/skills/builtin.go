package skills

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed builtin/*/SKILL.md
var builtinSkillsFS embed.FS

// BuiltinSkills returns the skills embedded directly in the QG binary.
func BuiltinSkills() []Skill {
	entries, err := fs.ReadDir(builtinSkillsFS, "builtin")
	if err != nil {
		return nil
	}
	out := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest := path.Join("builtin", entry.Name(), skillFileName)
		data, err := builtinSkillsFS.ReadFile(manifest)
		if err != nil {
			continue
		}
		out = append(out, parseSkill(entry.Name(), manifest, string(data)))
	}
	sort.Slice(out, func(left, right int) bool {
		return out[left].Name < out[right].Name
	})
	return out
}

// MergeBuiltins appends embedded skills not already present in loaded and
// returns the combined slice sorted by name. User, agent, and plugin skills
// take precedence over built-ins with the same name.
func MergeBuiltins(loaded []Skill) []Skill {
	return mergeBuiltins(loaded, true)
}

// MergeBuiltinsListed is like MergeBuiltins but strips Content from appended
// built-in entries, matching List/ListFromRoots callers.
func MergeBuiltinsListed(listed []Skill) []Skill {
	return mergeBuiltins(listed, false)
}

func mergeBuiltins(existing []Skill, keepContent bool) []Skill {
	seen := make(map[string]bool, len(existing))
	merged := make([]Skill, 0, len(existing)+1)
	for _, skill := range existing {
		seen[skill.Name] = true
		merged = append(merged, skill)
	}
	for _, builtin := range BuiltinSkills() {
		if seen[builtin.Name] {
			continue
		}
		if !keepContent {
			builtin.Content = ""
		}
		merged = append(merged, builtin)
	}
	sort.Slice(merged, func(left, right int) bool {
		return merged[left].Name < merged[right].Name
	})
	return merged
}

// IsBuiltinPath reports whether a skill Path refers to an embedded built-in skill.
func IsBuiltinPath(p string) bool {
	return strings.HasPrefix(p, "builtin/")
}
