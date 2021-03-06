package utils

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v2"
)

type statistics struct {
	PrivateProjectCount int `json:"private_project_count"`
	PrivateRepoCount    int `json:"private_repo_count"`
	PublicProjectCount  int `json:"public_project_count"`
	PublicRepoCount     int `json:"public_repo_count"`
	TotalProjectCount   int `json:"total_project_count"`
	TotalRepoCount      int `json:"total_repo_count"`
}

var stats statistics

type repoTop struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ProjectID    int    `json:"project_id"`
	Description  string `json:"description"`
	PullCount    int    `json:"pull_count"`
	StarCount    int    `json:"star_count"`
	TagsCount    int    `json:"tags_count"`
	CreationTime string `json:"creation_time"`
	UpdateTime   string `json:"update_time"`
}

var repos []*repoTop

type repoSearch struct {
	ProjectID      int    `json:"project_id"`
	ProjectName    string `json:"project_name"`
	ProjectPublic  bool   `json:"project_public"`
	PullCount      int    `json:"pull_count"`
	RepositoryName string `json:"repository_name"`
	TagsCount      int    `json:"tags_count"`
}

type searchRsp struct {
	Repository []*repoSearch `json:"repository"`
	Project    []interface{} `json:"project"`
}

var scRsp searchRsp

type tagInfo struct {
	Digest        string `json:"digest"`
	Name          string `json:"name"`
	Architecture  string `json:"architecture"`
	DockerVersion string `json:"docker_version"`
	Author        string `json:"author"`
	Created       string `json:"created"`
	Signature     string `json:"signature"`
}

type tagListRsp []*tagInfo

var tlRsp tagListRsp

func init() {
	Parser.AddCommand("rp_repos",
		"Delete repos by retention policy.",
		"Run retention policy analysis on Repositories, do soft deletion as you command, prompt user performing a GC.",
		&reposRP)
	Parser.AddCommand("rp_tags",
		"Delete tags of repo by retention policy.",
		"Run retention policy analysis on tags, and do deletion as you command.",
		&tagsRP)
}

type reposRetentionPolicy struct {
}

var reposRP reposRetentionPolicy

func (x *reposRetentionPolicy) Execute(args []string) error {
	if err := repoAnalyse(); err != nil {
		os.Exit(1)
	}
	if err := repoErase(); err != nil {
		os.Exit(1)
	}
	rpGCHint()
	return nil
}

type tagsRetentionPolicy struct {
	Day      int    `short:"d" long:"day" description:"(REQUIRED) The tags of a repository created less than N days should not be deleted." required:"yes"`
	Max      int    `short:"m" long:"max" description:"(REQUIRED) The maximum quantity of tags created more than N days of a repository should keep untouched." required:"yes"`
	RepoName string `short:"n" long:"repo_name" description:"Repo name for specific target. If not set, rp_tags will do jobs on all repos." default:""`
	DryRun   bool   `long:"dry-run" description:"Just analyzing, no actual deleting."`
}

var tagsRP tagsRetentionPolicy

func (x *tagsRetentionPolicy) Execute(args []string) error {
	if err := tagAnalyseAndErase(); err != nil {
		os.Exit(1)
	}
	return nil
}

func tagAnalyseAndErase() error {
	fmt.Println("===============================")
	fmt.Println("==  Start tags RP Analysing  ==")
	fmt.Println("===============================")
	fmt.Println()

	c, err := CookieLoad()
	if err != nil {
		fmt.Println("error:", err)
		return err
	}

	// By "/api/search", you can obtain all the items of projects and repositories
	// By setting "q=" query parameter, you can obtain ALL items.
	// By setting "q=<xxx>" query parameter, you can obtain items filtered by <xxx>.
	// But there exists a bug with it, so can not fulfill procession on specific repo correctly based on this now
	searchURL := URLGen("/api/search") + "?q=" + tagsRP.RepoName
	fmt.Println("--------------------")
	fmt.Println("==> GET", searchURL)

	_, _, errs := Request.Get(searchURL).
		Set("Cookie", "harbor-lang=zh-cn; beegosessionID="+c.BeegosessionID).
		EndStruct(&scRsp)
	for _, e := range errs {
		if e != nil {
			fmt.Println("error:", e)
			return e
		}
	}

	if tagsRP.RepoName == "" {
		fmt.Printf("==> on all Repos, max-days-untouched: %d   max-keep-num-after-Ndays: %d\n",
			tagsRP.Day, tagsRP.Max)
	} else {
		fmt.Printf("==> only on Repo [%s], max-days-untouched: %d   max-keep-num-after-Ndays: %d\n",
			tagsRP.RepoName, tagsRP.Day, tagsRP.Max)
	}
	fmt.Println("--------------------")

	// iterate on all repositories
	for _, r := range scRsp.Repository {
		fmt.Println(" ")
		fmt.Println("------------------------------------------------------")
		fmt.Printf("| repo_name: %s | tags_count: %d |\n", r.RepositoryName, r.TagsCount)
		fmt.Println("------------------------------------------------------")
		fmt.Println("---")

		fmt.Println("+--------+----------------------------------------------------+----------------------------------+-----------------+")
		fmt.Printf("| % -6s | % -50s | % -32s | % -15s |\n", "Action", "TagName", "CreateTime", "DaysPast")
		fmt.Println("+--------+----------------------------------------------------+----------------------------------+-----------------+")

		// obtain tags info of echo repo
		tagsListURL := URLGen("/api/repositories") + "/" + r.RepositoryName + "/tags"

		_, _, errs := Request.Get(tagsListURL).
			Set("Cookie", "harbor-lang=zh-cn; beegosessionID="+c.BeegosessionID).
			EndStruct(&tlRsp)
		for _, e := range errs {
			if e != nil {
				fmt.Println("error:", e)
				return e
			}
		}

		// heap sort on tags of echo repo
		tagmh = tagminheap{}
		heap.Init(&tagmh)
		for _, t := range tlRsp {
			//fmt.Printf("==> name: %s    created: %s\n", t.Name, t.Created)

			tagC := rfc3339Transform(t.Created)
			dayPast := time.Now().Sub(tagC).Hours() / 24

			// a. by each repo, tags created less than N days keep untouched
			if tagsRP.Day < int(dayPast) {
				it := &tagItem{
					tagName:   t.Name,
					timestamp: tagC.Unix(),
				}
				// tags created more than N days sort by minheap
				//fmt.Printf("[PUSH] %s <==> create: %s    dayPast: %f\n", it.tagName, t.Created, dayPast)
				fmt.Printf("| % -6s | % -50s | % -32s | % -15f |\n", "*", t.Name, t.Created, dayPast)
				heap.Push(&tagmh, it)
			} else {
				//fmt.Printf("[noPUSH] %s <==> create: %s    dayPast: %f\n", t.Name, t.Created, dayPast)
				fmt.Printf("| % -6s | % -50s | % -32s | % -15f |\n", "", t.Name, t.Created, dayPast)
			}

		}
		// b. by each repo, tags created more than N days keep Max number untouched.
		gtNdays := tagmh.Len()
		fmt.Println("+--------+----------------------------------------------------+----------------------------------+-----------------+")
		fmt.Printf("--> # of tags less than %d days: %d , # of tags more than %d days: %d\n",
			tagsRP.Day, r.TagsCount-gtNdays, tagsRP.Day, gtNdays)
		if gtNdays <= tagsRP.Max {
			fmt.Printf("--> max-keep-num-after-Ndays (%d) more than actual num (%d), so DO NOTHING.\n", tagsRP.Max, gtNdays)
		} else {
			fmt.Printf("--> max-keep-num-after-Ndays (%d) less than actual num (%d), so START DELETING.\n", tagsRP.Max, gtNdays)
			fmt.Println("---")
			if tagsRP.DryRun == false {
				for gtNdays > tagsRP.Max {
					it := heap.Pop(&tagmh).(*tagItem)
					fmt.Printf("[POP] %s <==> %d\n", it.tagName, it.timestamp)

					targetURL := URLGen("/api/repositories") + "/" + r.RepositoryName + "/tags/" + it.tagName
					fmt.Println("==> DELETE", targetURL)

					Request.Delete(targetURL).
						Set("Cookie", "harbor-lang=zh-cn; beegosessionID="+c.BeegosessionID).
						End(PrintStatus)

					gtNdays--
				}
			} else {
				fmt.Println("with '--dry-run' setting, just analyzing, no actual deleting.")
			}
		}
	}

	fmt.Printf("\n=== Finish tags RP Analysing ===\n\n")

	return nil
}

type retentionPolicy struct {
	UpdateTime struct {
		Base    float32 `yaml:"base" json:"base"`
		Factors []struct {
			Weight float32 `yaml:"weight" json:"weight"`
			Range  struct {
				Low  int `yaml:"low" json:"low"`
				High int `yaml:"high" json:"high"`
			} `yaml:"range" json:"range"`
		} `yaml:"factors" json:"factors"`
	} `yaml:"update_time" json:"update_time"`
	PullCount struct {
		Base    float32 `yaml:"base" json:"base"`
		Factors []struct {
			Weight float32 `yaml:"weight" json:"weight"`
			Range  struct {
				Low  int `yaml:"low" json:"low"`
				High int `yaml:"high" json:"high"`
			} `yaml:"range" json:"range"`
		} `yaml:"factors" json:"factors"`
	} `yaml:"pull_count" json:"pull_count"`
	TagsCount struct {
		Base    float32 `yaml:"base" json:"base"`
		Factors []struct {
			Weight float32 `yaml:"weight" json:"weight"`
			Range  struct {
				Low  int `yaml:"low" json:"low"`
				High int `yaml:"high" json:"high"`
			} `yaml:"range" json:"range"`
		} `yaml:"factors" json:"factors"`
	} `yaml:"tags_count" json:"tags_count"`
}

var rpfile = "./rp.yaml"

// rpLoad loads retention policy settings from rp.yaml
func rpLoad() (*retentionPolicy, error) {
	var rp retentionPolicy

	dataBytes, err := ioutil.ReadFile(rpfile)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal([]byte(dataBytes), &rp)
	if err != nil {
		return nil, err
	}

	return &rp, nil
}

// format exhibits current retention policy settings
func format(rp *retentionPolicy) {
	rp, err := rpLoad()
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	rps, err := json.MarshalIndent(rp, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("===>", string(rps))
}

// rfc3339Transform parses timestamp string as RFC3339 layout
func rfc3339Transform(in string) time.Time {
	t, err := time.Parse(time.RFC3339, in)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	return t
}

// grade calculates the score of each repo according to retention policy
func grade(r *repoTop, rp *retentionPolicy) float32 {
	var uf, pf, tf float32

	day := time.Now().Sub(rfc3339Transform(r.UpdateTime)).Hours() / 24
	for _, f := range rp.UpdateTime.Factors {
		if f.Range.Low <= int(day) && int(day) < f.Range.High {
			uf = f.Weight
			break
		}
	}
	if uf == float32(0) {
		fmt.Printf("Out of range: day = %.2f, uf is 0.0\n", day)
		uf = float32(0)
	}

	for _, f := range rp.PullCount.Factors {
		if f.Range.Low <= r.PullCount && r.PullCount < f.Range.High {
			pf = f.Weight
			break
		}
	}
	if pf == float32(0) {
		fmt.Printf("Out of range: pull_count = %d, pf is 1.0\n", r.PullCount)
		pf = float32(1)
	}

	for _, f := range rp.TagsCount.Factors {
		if f.Range.Low <= r.TagsCount && r.TagsCount < f.Range.High {
			tf = f.Weight
			break
		}
	}
	if tf == float32(0) {
		fmt.Printf("Out of range: tags_count = %d, tf is 1.0\n", r.TagsCount)
		tf = float32(1)
	}

	score := rp.UpdateTime.Base*uf + rp.PullCount.Base*pf + rp.TagsCount.Base*tf
	fmt.Printf("[factors] ==> score = UpdateTimeBase*uf + PullCountBase*pf + TagsCountBase*tf = %.2f * %.2f + %.2f * %.2f + %.2f * %.2f = %.2f   repo_id: %d\n",
		rp.UpdateTime.Base, uf, rp.PullCount.Base, pf, rp.TagsCount.Base, tf, score, r.ID)

	return score
}

// repoAnalyse calculates scores and output topN element by minheap sort
func repoAnalyse() error {

	rp, err := rpLoad()
	if err != nil {
		fmt.Println("error:", err)
		return err
	}

	// output current RP setting with pretty format
	//format(rp)

	statsURL := URLGen("/api/statistics")

	c, err := CookieLoad()
	if err != nil {
		fmt.Println("error:", err)
		return err
	}

	resp, _, statsErrs := Request.Get(statsURL).
		Set("Cookie", "harbor-lang=zh-cn; beegosessionID="+c.BeegosessionID).
		EndStruct(&stats)
	if resp.StatusCode != 200 {
		fmt.Printf("error: Expected StatusCode=200, actual StatusCode=%v\n", resp.StatusCode)
	}
	for _, e := range statsErrs {
		if e != nil {
			fmt.Println("error:", e)
			return e
		}
	}

	fmt.Println("-------------------------------------------------")
	fmt.Println("     Current Number of Public Repositories:", stats.PublicRepoCount)
	fmt.Println("-------------------------------------------------")

	topURL := URLGen("/api/repositories/top") + "?count=" + strconv.Itoa(stats.PublicRepoCount)
	fmt.Println("==> GET", topURL)
	_, _, topErrs := Request.Get(topURL).EndStruct(&repos)
	for _, e := range topErrs {
		if e != nil {
			fmt.Println("error:", e)
			return e
		}
	}

	heap.Init(&minh)
	heap.Init(&mhBk)
	for _, r := range repos {
		sc := grade(r, rp)

		// NOTE: codes below only for debug
		// ===========
		/*
			rs, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				fmt.Println("error:", err)
				return err
			}
			fmt.Printf("score (%f) =>\n%s\n", sc, rs)
		*/
		// ===========

		it := &repoItem{
			data:  r,
			score: sc,
		}
		heap.Push(&minh, it)
		heap.Push(&mhBk, it)
	}

	fmt.Println("------------------------------------------------------------------------------------------------------")
	fmt.Printf("      By the Rank of Scores (from low to high) , Suggestion on Deletion of public repos as follow\n")
	fmt.Println("------------------------------------------------------------------------------------------------------")

	for mhBk.Len() > 0 {
		it := heap.Pop(&mhBk).(*repoItem)
		fmt.Printf("%.2f <==> %+v\n", it.score, *it.data)
	}

	return nil
}

// repoErase implements soft deletion
func repoErase() error {

	var num int
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("\nPlease input the number of repo you wish to delete: ")
	for scanner.Scan() {
		in, err := strconv.Atoi(scanner.Text())
		if err != nil {
			fmt.Print("Invalid number, please input again: ")
			continue
		}
		fmt.Println("The number you input: ", in)

		fmt.Print("Confirm [y/n]: ")

		if !scanner.Scan() {
			break
		}
		confirm := scanner.Text()

		if strings.EqualFold(confirm, "y") {
			num = in
			break
		} else {
			fmt.Print("Please input the number of repo you wish to delete again: ")
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("error:", err)
		return err
	}

	if num <= 0 || num > 50 {
		fmt.Println("[Warning] The valid number range is (0, 50].")
		fmt.Println("[Warning] Sorry, you're not allowed proceeding... Abort.")
		return fmt.Errorf("error: the number is out of range")
	}

	fmt.Printf("\n=== Start soft deletion ===\n\n")

	for num > 0 {
		if minh.Len() > 0 {
			it := heap.Pop(&minh).(*repoItem)

			// NOTE: Before repo deletion running, you must login first. Codes here doing deletion directly without checking.
			targetURL := URLGen("/api/repositories") + "/" + it.data.Name
			fmt.Println("==> DELETE", targetURL)

			c, err := CookieLoad()
			if err != nil {
				fmt.Println("Error:", err)
				return err
			}

			Request.Delete(targetURL).
				Set("Cookie", "harbor-lang=zh-cn; beegosessionID="+c.BeegosessionID).
				End(PrintStatus)
		}
		num--
	}

	fmt.Printf("\n=== Finish soft deletion ===\n\n")
	return nil
}

// rpGCHint gives a hint about hard deletion.
func rpGCHint() {

	fmt.Println("-----------------------------")
	fmt.Println("You have finished 'soft deletion' stage，if you wish to free disk space effectively，you should:")
	fmt.Println("1. Enter into harbor's main installation directory (e.g. /opt/apps/harbor/)")
	fmt.Println("2. Run the following commands to preview which files/images will be deleted:")
	fmt.Println("    a. docker-compose stop")
	fmt.Println("    b. docker run -it --name gc --rm --volumes-from registry vmware/registry:2.6.2-photon garbage-collect --dry-run /etc/registry/config.yml")
	fmt.Println("3. Run the following commands to trigger GC operation:")
	fmt.Println("    a. docker run -it --name gc --rm --volumes-from registry vmware/registry:2.6.2-photon garbage-collect  /etc/registry/config.yml")
	fmt.Println("    b. docker-compose start")
	fmt.Println("")
	fmt.Println("WARNING:\nMake sure that no one is pushing images or Harbor is not running at all before you perform a GC. If someone were pushing an image while GC is running, there is a risk that the image's layers will be mistakenly deleted which results in a corrupted image. So before running GC, a preferred approach is to stop Harbor first.")
	fmt.Println("-----------------------------")
}
