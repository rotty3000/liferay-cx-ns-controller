package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zeebo/xxh3"
)

var regex = regexp.MustCompile("^[a-zA-Z0-9]+$")

func VirtuaInstanceIdToNamespace(virtualInstanceID string, applicationAlias string) (string, error) {
	if len(applicationAlias) > 16 {
		return "", fmt.Errorf("invalid applicationAlias: %s, length is greater than 16", applicationAlias)
	}

	if !regex.MatchString(applicationAlias) {
		return "", fmt.Errorf("invalid applicationAlias: %s, must only contain alphanumeric characters", applicationAlias)
	}

	return fmt.Sprintf("cx-%s-%s", applicationAlias, VirtuaInstanceIdToNamespaceSegment(virtualInstanceID, len(applicationAlias))), nil
}

func VirtuaInstanceIdToNamespaceSegment(virtualInstanceID string, suffixLength int) string {
	virtualInstanceID = strings.ReplaceAll(virtualInstanceID, ".", "")

	allowedLength := 63 - 4 - suffixLength
	if len(virtualInstanceID) > allowedLength {
		prefix := virtualInstanceID[0:(allowedLength - 16 - 2)]
		virtualInstanceID = prefix + "--" + Xxhash3(virtualInstanceID)
	}

	return virtualInstanceID
}

func Xxhash3(data string) string {
	if data == "" {
		return "0000000000000000"
	}

	return fmt.Sprintf("%016s", strconv.FormatUint(xxh3.HashString(data), 10)[0:16])
}
