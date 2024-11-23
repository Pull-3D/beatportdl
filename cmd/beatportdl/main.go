package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"Pull-3D/beatportdl/config"
	"Pull-3D/beatportdl/internal/beatport"
)

const (
	configFilename = "beatportdl-config.yml"
	cacheFilename  = "beatportdl-credentials.json"
	authUrl        = "https://api.beatport.com/v4/auth/o/authorize/?client_id=ryZ8LuyQVPqbK2mBX2Hwt4qSMtnWuTYSqBPO92yQ&response_type=code"
)

type application struct {
	config *config.AppConfig
	bp     *beatport.Beatport
	wg     sync.WaitGroup
}

func main() {
	configFilePath, err := FindFile(configFilename)
	if err != nil {
		fmt.Println("Config file not found, creating a new one")
		configFilePath, err = ExecutableDirFilePath(configFilename)
		if err != nil {
			FatalError("get executable path", err)
		}

		fmt.Print("Username: ")
		username := GetLine()
		fmt.Print("Password: ")
		password := GetLine()
		fmt.Print("Downloads directory: ")
		downloadsDir := GetLine()

		cfg := &config.AppConfig{
			Username:           username,
			Password:           password,
			DownloadsDirectory: downloadsDir,
		}
		if err := cfg.Save(configFilePath); err != nil {
			FatalError("save config", err)
		}
	}

	parsedConfig, err := config.Parse(configFilePath)
	if err != nil {
		FatalError("load config", err)
	}

	execCachePath, err := ExecutableDirFilePath(cacheFilename)
	if err != nil {
		FatalError("get executable path", err)
	}
	cacheFilePath := execCachePath

	_, err = os.Stat(cacheFilePath)
	if err != nil {
		workingCachePath, err := WorkingDirFilePath(cacheFilename)
		if err != nil {
			FatalError("get current working dir", err)
		}
		_, err = os.Stat(workingCachePath)
		if err == nil {
			cacheFilePath = workingCachePath
		}
	}

	bpClient, err := beatport.New(
		parsedConfig.Username,
		parsedConfig.Password,
		cacheFilePath,
		parsedConfig.Proxy,
	)
	if err != nil {
		FatalError("beatport api client", err)
	}

	if err := bpClient.LoadCachedTokenPair(); err != nil {
		if err := bpClient.NewTokenPair(); err != nil {
			FatalError("beatport", err)
		}
	}

	app := &application{
		config: parsedConfig,
		bp:     bpClient,
	}

	flag.Parse()
	inputArgs := flag.Args()

	var urls []string

	for _, arg := range inputArgs {
		if strings.HasSuffix(arg, ".txt") {
			file, err := os.Open(arg)
			if err != nil {
				FatalError("read input text file", err)
			}
			scanner := bufio.NewScanner(file)
			scanner.Split(bufio.ScanLines)

			for scanner.Scan() {
				urls = append(urls, scanner.Text())
			}

			file.Close()
		} else {
			urls = append(urls, arg)
		}
	}

	for {
		if len(urls) == 0 {
			fmt.Print("Enter track or release link or search query: ")
			input := GetLine()
			if strings.HasPrefix(input, "https://www.beatport.com") {
				urls = append(urls, input)
			} else {
				results, err := bpClient.Search(input)
				if err != nil {
					FatalError("beatport", err)
				}
				trackResultsLen := len(results.Tracks)
				releasesResultsLen := len(results.Releases)

				if trackResultsLen+releasesResultsLen == 0 {
					fmt.Println("No results found")
					continue
				}

				fmt.Println("Search results:")
				fmt.Println("[ Tracks ]")
				for i, track := range results.Tracks {
					fmt.Printf(
						"%2d. %s - %s (%s) [%s]\n", i+1,
						track.ArtistsDisplay(beatport.ArtistTypeMain),
						track.Name,
						track.MixName,
						track.Length,
					)
				}
				fmt.Println("\n[ Releases ]")
				indexOffset := trackResultsLen + 1
				for i, release := range results.Releases {
					fmt.Printf(
						"%2d. %s - %s [%s]\n", i+indexOffset,
						release.ArtistsDisplay(beatport.ArtistTypeMain),
						release.Name,
						release.Label.Name,
					)
				}
				fmt.Print("Enter the result number(s): ")
				input = GetLine()
				requestedResults := strings.Split(input, " ")
				for _, result := range requestedResults {
					resultInt, err := strconv.Atoi(result)
					if err != nil {
						FatalError("invalid result number", err)
					}

					if resultInt > releasesResultsLen+trackResultsLen || resultInt == 0 {
						fmt.Printf("invalid result number: %d\n", resultInt)
						continue
					}

					if resultInt >= indexOffset {
						urls = append(urls, results.Releases[resultInt-indexOffset].URL)
					} else {
						urls = append(urls, results.Tracks[resultInt-1].URL)
					}
				}
			}
		}

		for _, input := range urls {
			app.background(func() {
				link, err := app.bp.ParseUrl(input)
				if err != nil {
					LogError("parse url", err)
					return
				}
				if link.Type == beatport.TrackLink {
					downloadsDirectory := app.config.DownloadsDirectory
					track, err := app.bp.GetTrack(link.ID)
					if err != nil {
						LogError("fetch track", err)
						return
					}

					var coverPath string
					var coverUrl string

					if app.config.CreateReleaseDirectory {
						release, err := app.bp.GetRelease(track.Release.ID)
						if err != nil {
							LogError("fetch release", err)
							return
						}
						releaseDirectory := release.DirectoryName(
							app.config.ReleaseDirectoryTemplate,
							app.config.WhitespaceCharacter,
						)
						downloadsDirectory = fmt.Sprintf("%s/%s",
							downloadsDirectory,
							releaseDirectory,
						)
						if app.config.CoverSize != "" {
							coverUrl = strings.Replace(
								release.Image.DynamicURI,
								"{w}x{h}",
								app.config.CoverSize,
								-1,
							)
							coverPath = downloadsDirectory + "/cover.jpg"
						}
					}

					if err := CreateDirectory(downloadsDirectory); err != nil {
						LogError("create downloads directory", err)
						return
					}

					if coverUrl != "" && coverPath != "" {
						if err = app.downloadFile(coverUrl, coverPath); err != nil {
							LogError("download cover", err)
						}
					}

					if err := app.saveTrack(*track, downloadsDirectory, app.config.Quality); err != nil {
						LogError("save track", err)
						return
					}

				} else if link.Type == beatport.ReleaseLink {
					release, err := app.bp.GetRelease(link.ID)
					if err != nil {
						LogError("fetch release", err)
						return
					}

					downloadsDirectory := app.config.DownloadsDirectory
					if app.config.CreateReleaseDirectory {
						releaseDirectory := release.DirectoryName(
							app.config.ReleaseDirectoryTemplate,
							app.config.WhitespaceCharacter,
						)
						downloadsDirectory = fmt.Sprintf("%s/%s",
							app.config.DownloadsDirectory,
							releaseDirectory,
						)
					}

					if err := CreateDirectory(downloadsDirectory); err != nil {
						LogError("create downloads directory", err)
						return
					}

					if app.config.CoverSize != "" {
						coverUrl := strings.Replace(
							release.Image.DynamicURI,
							"{w}x{h}",
							app.config.CoverSize,
							-1,
						)
						coverPath := downloadsDirectory + "/cover.jpg"
						if err = app.downloadFile(coverUrl, coverPath); err != nil {
							LogError("download cover", err)
						}
					}

					for _, trackUrl := range release.TrackUrls {
						app.background(func() {
							trackLink, _ := app.bp.ParseUrl(trackUrl)
							track, err := app.bp.GetTrack(trackLink.ID)
							if err != nil {
								LogError("fetch track", err)
								return
							}
							if err := app.saveTrack(*track, downloadsDirectory, app.config.Quality); err != nil {
								LogError("save track", err)
								return
							}
						})
					}
				}
			})
		}

		app.wg.Wait()

		urls = []string{}
	}
}

func (app *application) saveTrack(track beatport.Track, directory string, quality string) error {
	stream, err := app.bp.DownloadTrack(track.ID, quality)
	if err != nil {
		return err
	}
	fileName := track.Filename(app.config.TrackFileTemplate, app.config.WhitespaceCharacter)
	var fileExtension string
	var displayQuality string
	switch stream.StreamQuality {
	case ".128k.aac.mp4":
		fileExtension = ".aac"
		displayQuality = "AAC 128kbps"
	case ".256k.aac.mp4":
		fileExtension = ".aac"
		displayQuality = "AAC 256kbps"
	case ".flac":
		fileExtension = ".flac"
		displayQuality = "FLAC"
	default:
		return fmt.Errorf("invalid stream quality: %s", stream.StreamQuality)
	}
	fmt.Printf("Downloading %s (%s) [%s]\n", track.Name, track.MixName, displayQuality)
	filePath := fmt.Sprintf("%s/%s%s", directory, fileName, fileExtension)
	if err = app.downloadFile(stream.Location, filePath); err != nil {
		return err
	}
	fmt.Printf("Finished downloading %s (%s) [%s]\n", track.Name, track.MixName, displayQuality)

	return nil
}
