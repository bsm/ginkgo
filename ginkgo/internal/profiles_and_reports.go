package internal

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/bsm/ginkgo/v2/reporters"
	"github.com/bsm/ginkgo/v2/types"
)

func AbsPathForGeneratedAsset(assetName string, suite TestSuite, cliConfig types.CLIConfig, process int) string {
	suffix := ""
	if process != 0 {
		suffix = fmt.Sprintf(".%d", process)
	}
	if cliConfig.OutputDir == "" {
		return filepath.Join(suite.AbsPath(), assetName+suffix)
	}
	outputDir, _ := filepath.Abs(cliConfig.OutputDir)
	return filepath.Join(outputDir, suite.NamespacedName()+"_"+assetName+suffix)
}

func FinalizeProfilesAndReportsForSuites(suites TestSuites, cliConfig types.CLIConfig, suiteConfig types.SuiteConfig, reporterConfig types.ReporterConfig, goFlagsConfig types.GoFlagsConfig) ([]string, error) {
	messages := []string{}
	suitesWithProfiles := suites.WithState(TestSuiteStatePassed, TestSuiteStateFailed) //anything else won't have actually run and generated a profile

	// merge cover profiles if need be
	if goFlagsConfig.Cover && !cliConfig.KeepSeparateCoverprofiles {
		coverProfiles := []string{}
		for _, suite := range suitesWithProfiles {
			if !suite.HasProgrammaticFocus {
				coverProfiles = append(coverProfiles, AbsPathForGeneratedAsset(goFlagsConfig.CoverProfile, suite, cliConfig, 0))
			}
		}

		if len(coverProfiles) > 0 {
			dst := goFlagsConfig.CoverProfile
			if cliConfig.OutputDir != "" {
				dst = filepath.Join(cliConfig.OutputDir, goFlagsConfig.CoverProfile)
			}
			err := MergeAndCleanupCoverProfiles(coverProfiles, dst)
			if err != nil {
				return messages, err
			}
			coverage, err := GetCoverageFromCoverProfile(dst)
			if err != nil {
				return messages, err
			}
			if coverage == 0 {
				messages = append(messages, "composite coverage: [no statements]")
			} else if suitesWithProfiles.AnyHaveProgrammaticFocus() {
				messages = append(messages, fmt.Sprintf("composite coverage: %.1f%% of statements however some suites did not contribute because they included programatically focused specs", coverage))
			} else {
				messages = append(messages, fmt.Sprintf("composite coverage: %.1f%% of statements", coverage))
			}
		} else {
			messages = append(messages, "no composite coverage computed: all suites included programatically focused specs")
		}
	}

	// copy binaries if need be
	for _, suite := range suitesWithProfiles {
		if goFlagsConfig.BinaryMustBePreserved() && cliConfig.OutputDir != "" {
			src := suite.PathToCompiledTest
			dst := filepath.Join(cliConfig.OutputDir, suite.NamespacedName()+".test")
			if suite.Precompiled {
				if err := CopyFile(src, dst); err != nil {
					return messages, err
				}
			} else {
				if err := os.Rename(src, dst); err != nil {
					return messages, err
				}
			}
		}
	}

	type reportFormat struct {
		ReportName   string
		GenerateFunc func(types.Report, string) error
		MergeFunc    func([]string, string) ([]string, error)
	}
	reportFormats := []reportFormat{}
	if reporterConfig.JSONReport != "" {
		reportFormats = append(reportFormats, reportFormat{ReportName: reporterConfig.JSONReport, GenerateFunc: reporters.GenerateJSONReport, MergeFunc: reporters.MergeAndCleanupJSONReports})
	}
	if reporterConfig.JUnitReport != "" {
		reportFormats = append(reportFormats, reportFormat{ReportName: reporterConfig.JUnitReport, GenerateFunc: reporters.GenerateJUnitReport, MergeFunc: reporters.MergeAndCleanupJUnitReports})
	}
	if reporterConfig.TeamcityReport != "" {
		reportFormats = append(reportFormats, reportFormat{ReportName: reporterConfig.TeamcityReport, GenerateFunc: reporters.GenerateTeamcityReport, MergeFunc: reporters.MergeAndCleanupTeamcityReports})
	}

	// Generate reports for suites that failed to run
	reportableSuites := suites.ThatAreGinkgoSuites()
	for _, suite := range reportableSuites.WithState(TestSuiteStateFailedToCompile, TestSuiteStateFailedDueToTimeout, TestSuiteStateSkippedDueToPriorFailures, TestSuiteStateSkippedDueToEmptyCompilation) {
		report := types.Report{
			SuitePath:      suite.AbsPath(),
			SuiteConfig:    suiteConfig,
			SuiteSucceeded: false,
		}
		switch suite.State {
		case TestSuiteStateFailedToCompile:
			report.SpecialSuiteFailureReasons = append(report.SpecialSuiteFailureReasons, suite.CompilationError.Error())
		case TestSuiteStateFailedDueToTimeout:
			report.SpecialSuiteFailureReasons = append(report.SpecialSuiteFailureReasons, TIMEOUT_ELAPSED_FAILURE_REASON)
		case TestSuiteStateSkippedDueToPriorFailures:
			report.SpecialSuiteFailureReasons = append(report.SpecialSuiteFailureReasons, PRIOR_FAILURES_FAILURE_REASON)
		case TestSuiteStateSkippedDueToEmptyCompilation:
			report.SpecialSuiteFailureReasons = append(report.SpecialSuiteFailureReasons, EMPTY_SKIP_FAILURE_REASON)
			report.SuiteSucceeded = true
		}

		for _, format := range reportFormats {
			format.GenerateFunc(report, AbsPathForGeneratedAsset(format.ReportName, suite, cliConfig, 0))
		}
	}

	// Merge reports unless we've been asked to keep them separate
	if !cliConfig.KeepSeparateReports {
		for _, format := range reportFormats {
			reports := []string{}
			for _, suite := range reportableSuites {
				reports = append(reports, AbsPathForGeneratedAsset(format.ReportName, suite, cliConfig, 0))
			}
			dst := format.ReportName
			if cliConfig.OutputDir != "" {
				dst = filepath.Join(cliConfig.OutputDir, format.ReportName)
			}
			mergeMessages, err := format.MergeFunc(reports, dst)
			messages = append(messages, mergeMessages...)
			if err != nil {
				return messages, err
			}
		}
	}

	return messages, nil
}

//loads each profile, combines them, deletes them, stores them in destination
func MergeAndCleanupCoverProfiles(profiles []string, destination string) error {
	combined := &bytes.Buffer{}
	modeRegex := regexp.MustCompile(`^mode: .*\n`)
	for i, profile := range profiles {
		contents, err := os.ReadFile(profile)
		if err != nil {
			return fmt.Errorf("Unable to read coverage file %s:\n%s", profile, err.Error())
		}
		os.Remove(profile)

		// remove the cover mode line from every file
		// except the first one
		if i > 0 {
			contents = modeRegex.ReplaceAll(contents, []byte{})
		}

		_, err = combined.Write(contents)

		// Add a newline to the end of every file if missing.
		if err == nil && len(contents) > 0 && contents[len(contents)-1] != '\n' {
			_, err = combined.Write([]byte("\n"))
		}

		if err != nil {
			return fmt.Errorf("Unable to append to coverprofile:\n%s", err.Error())
		}
	}

	err := os.WriteFile(destination, combined.Bytes(), 0666)
	if err != nil {
		return fmt.Errorf("Unable to create combined cover profile:\n%s", err.Error())
	}
	return nil
}

func GetCoverageFromCoverProfile(profile string) (float64, error) {
	cmd := exec.Command("go", "tool", "cover", "-func", profile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("Could not process Coverprofile %s: %s", profile, err.Error())
	}
	re := regexp.MustCompile(`total:\s*\(statements\)\s*(\d*\.\d*)\%`)
	matches := re.FindStringSubmatch(string(output))
	if matches == nil {
		return 0, fmt.Errorf("Could not parse Coverprofile to compute coverage percentage")
	}
	coverageString := matches[1]
	coverage, err := strconv.ParseFloat(coverageString, 64)
	if err != nil {
		return 0, fmt.Errorf("Could not parse Coverprofile to compute coverage percentage: %s", err.Error())
	}

	return coverage, nil
}
