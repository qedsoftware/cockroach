// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/stretchr/testify/require"
)

var libPQReleaseTagRegex = regexp.MustCompile(`^v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<point>\d+)$`)
var libPQSupportedTag = "v1.10.0"

func registerLibPQ(r *testRegistry) {
	runLibPQ := func(ctx context.Context, t *test, c *cluster) {
		if c.isLocal() {
			t.Fatal("cannot be run in local mode")
		}
		node := c.Node(1)
		t.Status("setting up cockroach")
		c.Put(ctx, cockroach, "./cockroach", c.All())
		c.Start(ctx, t, c.All())

		version, err := fetchCockroachVersion(ctx, c, node[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := alterZoneConfigAndClusterSettings(
			ctx, version, c, node[0], nil,
		); err != nil {
			t.Fatal(err)
		}

		t.Status("cloning lib/pq and installing prerequisites")
		latestTag, err := repeatGetLatestTag(
			ctx, c, "lib", "pq", libPQReleaseTagRegex)
		require.NoError(t, err)
		c.l.Printf("Latest lib/pq release is %s.", latestTag)
		c.l.Printf("Supported lib/pq release is %s.", libPQSupportedTag)

		installGolang(ctx, t, c, node)

		const (
			libPQRepo   = "github.com/lib/pq"
			libPQPath   = goPath + "/src/" + libPQRepo
			resultsDir  = "~/logs/report/libpq-results"
			resultsPath = resultsDir + "/report.xml"
		)

		// Remove any old lib/pq installations
		err = repeatRunE(
			ctx, c, node, "remove old lib/pq", fmt.Sprintf("rm -rf %s", libPQPath),
		)
		require.NoError(t, err)

		// Install go-junit-report to convert test results to .xml format we know
		// how to work with.
		err = repeatRunE(ctx, c, node, "install go-junit-report",
			fmt.Sprintf("GOPATH=%s go get -u github.com/jstemmer/go-junit-report", goPath),
		)
		require.NoError(t, err)

		err = repeatGitCloneE(
			ctx,
			t.l,
			c,
			fmt.Sprintf("https://%s.git", libPQRepo),
			libPQPath,
			libPQSupportedTag,
			node,
		)
		require.NoError(t, err)
		_ = c.RunE(ctx, node, fmt.Sprintf("mkdir -p %s", resultsDir))

		blocklistName, expectedFailures, ignorelistName, ignoreList := libPQBlocklists.getLists(version)
		if expectedFailures == nil {
			t.Fatalf("No lib/pq blocklist defined for cockroach version %s", version)
		}
		c.l.Printf("Running cockroach version %s, using blocklist %s, using ignorelist %s", version, blocklistName, ignorelistName)

		t.Status("running lib/pq test suite and collecting results")

		// List all the tests that start with Test or Example.
		testListRegex := "^(Test|Example)"
		buf, err := c.RunWithBuffer(
			ctx,
			t.l,
			node,
			fmt.Sprintf(`cd %s && PGPORT=26257 PGUSER=root PGSSLMODE=disable PGDATABASE=postgres go test -list "%s"`, libPQPath, testListRegex),
		)
		require.NoError(t, err)

		// Convert the output of go test -list into an list.
		tests := strings.Fields(string(buf))
		var allowedTests []string

		for _, testName := range tests {
			// Ignore tests that do not match the test regex pattern.
			matched, err := regexp.MatchString(testListRegex, testName)
			require.NoError(t, err)
			if !matched {
				continue
			}
			// If the test is part of ignoreList, do not run the test.
			if _, ok := ignoreList[testName]; !ok {
				allowedTests = append(allowedTests, testName)
			}
		}

		allowedTestsRegExp := fmt.Sprintf(`"^(%s)$"`, strings.Join(allowedTests, "|"))

		// Ignore the error as there will be failing tests.
		_ = c.RunE(
			ctx,
			node,
			fmt.Sprintf("cd %s && PGPORT=26257 PGUSER=root PGSSLMODE=disable PGDATABASE=postgres go test -run %s -v 2>&1 | %s/bin/go-junit-report > %s",
				libPQPath, allowedTestsRegExp, goPath, resultsPath),
		)

		parseAndSummarizeJavaORMTestsResults(
			ctx, t, c, node, "lib/pq" /* ormName */, []byte(resultsPath),
			blocklistName, expectedFailures, ignoreList, version, latestTag,
		)
	}

	r.Add(testSpec{
		Name:       "lib/pq",
		Owner:      OwnerSQLExperience,
		MinVersion: "v20.1.0",
		Cluster:    makeClusterSpec(1),
		Tags:       []string{`default`, `driver`},
		Run:        runLibPQ,
	})
}
