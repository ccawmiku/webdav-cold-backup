package backup

import (
	"testing"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/object"
)

func TestPlanJobsKeepsLargeFileTailSeparateAndPacksSmallFiles(t *testing.T) {
	engine := &Engine{CacheRoot: t.TempDir()}
	task := model.Task{ID: "task", Salt: "salt", BlockSize: 40_000_000}
	large := preparedFile{source: object.SourceFile{ID: "large", Size: 50_000_000}}
	smallOne := preparedFile{source: object.SourceFile{ID: "small-1", Size: 1_000}}
	smallTwo := preparedFile{source: object.SourceFile{ID: "small-2", Size: 2_000}}
	jobs, err := engine.planJobs(task, make([]byte, 32), []preparedFile{large, smallOne, smallTwo})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 4 {
		t.Fatalf("expected three large parts plus one pack, got %d", len(jobs))
	}
	for index := 0; index < 3; index++ {
		if jobs[index].spec.Kind != "file" || len(jobs[index].spec.Slices) != 1 || jobs[index].spec.Slices[0].File.ID != "large" {
			t.Fatalf("large part mixed with another file: %+v", jobs[index])
		}
	}
	pack := jobs[3]
	if pack.spec.Kind != "pack" || len(pack.spec.Slices) != 2 {
		t.Fatalf("small files were not packed: %+v", pack)
	}
	if jobs[2].spec.Slices[0].Length >= jobs[0].spec.Slices[0].Length {
		t.Fatal("last large-file part should be a smaller independent tail")
	}
}
