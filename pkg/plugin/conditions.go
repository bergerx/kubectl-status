package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"k8s.io/klog/v2"
)

var (
	userAbnormalTrueConditionTypesOnce sync.Once
	userAbnormalTrueConditionTypes     userAbnormalTrueConditionTypeMatcher
)

// userAbnormalTrueConditionTypeMatcher holds condition types loaded from the user provided
// abnormal-true-condition-types file, split by match kind.
type userAbnormalTrueConditionTypeMatcher struct {
	exact    map[string]bool
	prefixes []string // from lines like "Unhealthy*"
	suffixes []string // from lines like "*Problematic"
}

func (m userAbnormalTrueConditionTypeMatcher) matches(conditionType string) bool {
	if m.exact[conditionType] {
		return true
	}
	for _, prefix := range m.prefixes {
		if strings.HasPrefix(conditionType, prefix) {
			return true
		}
	}
	for _, suffix := range m.suffixes {
		if strings.HasSuffix(conditionType, suffix) {
			return true
		}
	}
	return false
}

// userAbnormalTrueConditionTypeMatchers loads condition types from
// ~/.kubectl-status/abnormal-true-condition-types, one per line, so users can extend the
// hardcoded list of condition types below without recompiling. Blank lines and lines starting
// with "#" are ignored. A line may be an exact condition type, a suffix pattern like
// "*Problematic", or a prefix pattern like "Unhealthy*". Read once and cached for the lifetime
// of the process.
func userAbnormalTrueConditionTypeMatchers() userAbnormalTrueConditionTypeMatcher {
	userAbnormalTrueConditionTypesOnce.Do(func() {
		userAbnormalTrueConditionTypes = userAbnormalTrueConditionTypeMatcher{exact: map[string]bool{}}
		homeDir, err := os.UserHomeDir()
		if err != nil {
			klog.V(3).ErrorS(err, "error getting user home dir, ignoring")
			return
		}
		path := filepath.Join(homeDir, ".kubectl-status", "abnormal-true-condition-types")
		data, err := os.ReadFile(path)
		if err != nil {
			klog.V(5).ErrorS(err, "error reading user provided abnormal-true condition types file, ignoring", "path", path)
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "" || strings.HasPrefix(line, "#"):
				continue
			case strings.HasPrefix(line, "*"):
				userAbnormalTrueConditionTypes.suffixes = append(userAbnormalTrueConditionTypes.suffixes, strings.TrimPrefix(line, "*"))
			case strings.HasSuffix(line, "*"):
				userAbnormalTrueConditionTypes.prefixes = append(userAbnormalTrueConditionTypes.prefixes, strings.TrimSuffix(line, "*"))
			default:
				userAbnormalTrueConditionTypes.exact[line] = true
			}
		}
	})
	return userAbnormalTrueConditionTypes
}

func isStatusConditionHealthy(condition map[string]interface{}) bool {
	switch {
	/*
		From https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties:

		> Condition types should indicate state in the "abnormal-true" polarity. For example, if the condition indicates
		> when a policy is invalid, the "is valid" case is probably the norm, so the condition should be called
		> "Invalid".

		But apparently this is not common among most resources, so we have the list of cases that matches the expected
		behaviour rather than the exceptions.
	*/
	case strings.HasSuffix(fmt.Sprint(condition["type"]), "Pressure"), // Node Pressure conditions
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Unavailable"), // Node NetworkUnavailable condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Failure"),     // ReplicaSet ReplicaFailure: condition
		strings.HasPrefix(fmt.Sprint(condition["type"]), "Non"),         // CRD NonStructuralSchema condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Problem"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Error"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Errors"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Hung"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Missing"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Flapping"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Unhealthy"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Failed"), // Failed Jobs has this condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Warning"),
		strings.HasPrefix(fmt.Sprint(condition["type"]), "Corrupt"),

		// Conditions from "Node Problem Detector"
		condition["type"] == "DockerContainerStartupFailure",
		condition["type"] == "FilesystemIsReadOnly",
		condition["type"] == "KernelDeadlock",
		condition["type"] == "KernelOops",
		condition["type"] == "OOMKilling",
		condition["type"] == "ReadonlyFilesystem",
		condition["type"] == "UnregisterNetDevice",
		condition["type"] == "FrequentDockerRestart",
		condition["type"] == "FrequentContainerdRestart",
		condition["type"] == "FrequentKubeletRestart",
		condition["type"] == "RebootScheduled",
		condition["type"] == "TerminateScheduled",
		condition["type"] == "RedeployScheduled",
		condition["type"] == "PreemptScheduled",
		condition["type"] == "FreezeScheduled",
		condition["type"] == "FrequentUnregisterNetDevice",
		condition["type"] == "VMEventScheduled",
		condition["type"] == "NVLinkStatusInactive",
		condition["type"] == "KernelDeadLock", // legacy mis-capitalized variant seen in some NPD configs
		condition["type"] == "OutOfDisk",      // deprecated legacy Node condition, same polarity as DiskPressure

		// User provided additions, see ~/.kubectl-status/abnormal-true-condition-types
		userAbnormalTrueConditionTypeMatchers().matches(fmt.Sprint(condition["type"])):
		switch condition["status"] {
		case "False":
			return true
		case "True", "Unknown":
			return false
		default:
			// not likely to ever happen, but just in case
			return false
		}
	default:
		switch condition["status"] {
		case "True":
			return true
		case "False", "Unknown":
			return false
		default:
			return false
		}
	}
}
