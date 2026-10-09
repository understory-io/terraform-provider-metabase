package provider

import "regexp"

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(s)) }
