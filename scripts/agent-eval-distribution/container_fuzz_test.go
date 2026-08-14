package main

import "testing"

func FuzzValidateContainerImageArchiveNeverPanics(f *testing.F) {
	binary := testStaticEvaluatorBinary("fuzz evaluator\n")
	commit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	compatibilitySHA := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	dependencySHA := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	sourceTreeSHA := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	containerfileSHA := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	image, err := buildContainerImage(binary, commit, distributionContractVersion, "linux", "amd64", sha256Bytes(binary), compatibilitySHA, dependencySHA, sourceTreeSHA, containerfileSHA)
	if err != nil {
		f.Fatal(err)
	}
	want := containerImageExpectation{
		ManifestDigest: image.ManifestDigest, BinarySHA: sha256Bytes(binary), CompatibilitySHA: compatibilitySHA,
		DependencySHA: dependencySHA, SourceTreeSHA: sourceTreeSHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: commit, Version: distributionContractVersion, Platform: "linux", Architecture: "amd64",
	}
	f.Add(image.Archive)
	f.Add(append(append([]byte(nil), image.Archive...), 0))
	f.Add([]byte("not an OCI archive"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_ = validateContainerImageArchive(data, want)
	})
}
