package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zeebo/xxh3"
)

var regex = regexp.MustCompile("^[a-zA-Z0-9]+$")

func VirtualInstanceIdToNamespace(originNamespace string, virtualInstanceID string, applicationAlias string) (string, error) {
	if len(applicationAlias) > 16 {
		return "", fmt.Errorf("invalid applicationAlias: %s, length is greater than 16", applicationAlias)
	}

	if !regex.MatchString(applicationAlias) {
		return "", fmt.Errorf("invalid applicationAlias: %s, must only contain alphanumeric characters", applicationAlias)
	}

	virtualInstanceID = strings.ReplaceAll(virtualInstanceID, ".", "")

	lengthOfApplicationAlias := len(applicationAlias)
	originNamespaceHash := Xxhash3(originNamespace)
	virtualInstanceIDHash := Xxhash3(virtualInstanceID)

	if len(originNamespace) > (63 - 5 - lengthOfApplicationAlias) {
		originNamespace = originNamespace[:20] + originNamespaceHash
	}

	if len(virtualInstanceID) > (63 - 5 - lengthOfApplicationAlias - len(originNamespace)) {
		virtualInstanceID = virtualInstanceID[:(63-5-lengthOfApplicationAlias-len(originNamespace)-4)] + virtualInstanceIDHash
	}

	return fmt.Sprintf("cx-%s-%s-%s", originNamespace, applicationAlias, virtualInstanceID), nil
}

func Xxhash3(data string) string {
	if data == "" {
		return "0000"
	}

	return fmt.Sprintf("%04s", strconv.FormatUint(xxh3.HashString(data), 10)[0:4])
}
