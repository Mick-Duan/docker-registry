/*
Docker Push & Pull

执行 docker push 命令流程：
    1. docker 向 registry 服务器注册 repository： PUT /v1/repositories/<username>/<repository> -> PUTRepository()
    2. 参数是 JSON 格式的 <repository> 所有 image 的 id 列表，按照 image 的构建顺序排列。
    3. 根据 <repository> 的 <tags> 进行循环：
       3.1 获取 <image> 的 JSON 文件：GET /v1/images/<image_id>/json -> image.go#GETJSON()
       3.2 如果没有此文件或内容返回 404 。
       3.3 docker push 认为服务器没有 image 对应的文件，向服务器上传 image 相关文件。
           3.3.1 写入 <image> 的 JSON 文件：PUT /v1/images/<image_id>/json -> image.go#PUTJSON()
           3.3.2 写入 <image> 的 layer 文件：PUT /v1/images/<image_id>/layer -> image.go#PUTLayer()
           3.3.3 写入 <image> 的 checksum 信息：PUT /v1/images/<image_id>/checksum -> image.go#PUTChecksum()
       3.4 上传完此 tag 的所有 image 后，向服务器写入 tag 信息：PUT /v1/repositories/(namespace)/(repository)/tags/(tag) -> PUTTag()
    4. 所有 tags 的 image 上传完成后，向服务器发送所有 images 的校验信息，PUT /v1/repositories/(namespace)/(repo_name)/images -> PUTRepositoryImages()

执行 docker pull 命令流程：
    1. docker 访问 registry 服务器 repository 的 images 信息：GET /v1/repositories/<username>/<repository>/images -> GetRepositoryImages()
    2. docker 访问 registry 服务器 repository 的 tags 信息：GET /v1/repositoies/<username>/<repository>/tags -> GetRepositoryTags()
    3. 根据 <repository> 的 <tags> 中 image 信息进行循环：
      3.1 获取 <image> 的 Ancestry 信息：GET /v1/images/<image_id>/ancestry -> GetImageAncestry()
      3.2 获取 <image> 的 JSON 数据: GET /v1/images/<image_id>/json -> GetImageJson()
      3.3 获取 <image> 的 Layer 文件: GET /v1/images/<image_id/layer -> GetImageLayer()

*/
package controllers

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/astaxie/beego"
	"github.com/dockboard/docker-registry/models"
	"github.com/dockboard/docker-registry/utils"
)

type RepositoryController struct {
	beego.Controller
}

func (r *RepositoryController) URLMapping() {
	r.Mapping("PutTag", r.PutTag)
	r.Mapping("PutRepositoryImages", r.PutRepositoryImages)
	r.Mapping("GetRepositoryImages", r.GetRepositoryImages)
	r.Mapping("GetRepositoryTags", r.GetRepositoryTags)
	r.Mapping("PutRepository", r.PutRepository)
}

func (this *RepositoryController) Prepare() {
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Registry-Version", beego.AppConfig.String("docker::Version"))
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Registry-Config", beego.AppConfig.String("docker::Config"))

	beego.Trace("Authorization:" + this.Ctx.Input.Header("Authorization"))

	r, _ := regexp.Compile(`Token signature=([[:alnum:]]+),repository="([[:alnum:]]+)/([[:graph:]]+)",access=([[:alnum:]]+)`)
	authorizations := r.FindStringSubmatch(this.Ctx.Input.Header("Authorization"))

	beego.Trace("Authorizations Length: " + strconv.FormatInt(int64(len(authorizations)), 10))

	if len(authorizations) == 5 {
		token, _, username, _, _ := authorizations[0], authorizations[1], authorizations[2], authorizations[3], authorizations[4]

		beego.Trace("Token: " + token)
		beego.Trace("Username: " + username)

		user := &models.User{Username: username, Token: token}
		has, err := models.Engine.Get(user)

		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(401)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Unauthorized\"}"))
			this.StopRun()
		}

		if has == false || user.Actived == false {
			this.Ctx.Output.Context.Output.SetStatus(403)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"User is not exist or not actived.\"}"))
			this.StopRun()
		}

		this.Data["user"] = user

	} else {
		username, passwd, err := utils.DecodeBasicAuth(this.Ctx.Input.Header("Authorization"))

		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(401)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Unauthorized\"}"))
			this.StopRun()
		}

		beego.Trace("[Username & Password] " + username + " -> " + passwd)

		user := &models.User{Username: username, Password: passwd}
		has, err := models.Engine.Get(user)

		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(401)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Unauthorized\"}"))
			this.StopRun()
		}

		if has == false || user.Actived == false {
			this.Ctx.Output.Context.Output.SetStatus(403)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"User is not exist or not actived.\"}"))
			this.StopRun()
		}

		this.Data["user"] = user
	}
}

func (this *RepositoryController) PutRepository() {

	user := this.Data["user"].(*models.User)

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Trace("[Namespace & Repository] " + namespace + " -> " + repository)

	//判断用户的username和namespace是否相同
	if user.Username != namespace {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"username != namespace.\"}"))
		this.StopRun()
	}

	//创建token并保存
	//需要加密的字符串为 UserName+UserPassword+时间戳
	md5String := fmt.Sprintf("%v%v%v", user.Username, user.Password, string(time.Now().Unix()))
	h := md5.New()
	h.Write([]byte(md5String))
	signature := hex.EncodeToString(h.Sum(nil))
	token := fmt.Sprintf("Token signature=%v,repository=\"%v/%v\",access=write", signature, namespace, repository)

	beego.Trace("[Token] " + token)

	//保存Token
	user.Token = token
	_, err := models.Engine.Id(user.Id).Cols("Token").Update(user)

	if err != nil {
		beego.Trace(err)
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update token error.\"}"))
		this.StopRun()
	}

	//创建或更新 Repository 数据
	//也可以采用 ioutil.ReadAll(this.Ctx.Request.Body) 的方式读取 body 数据
	beego.Trace("[Request Body] " + string(this.Ctx.Input.CopyBody()))

	repo := &models.Repository{Namespace: namespace, Repository: repository}
	has, err := models.Engine.Get(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	repo.JSON = string(this.Ctx.Input.CopyBody())

	if has == true {

		_, err := models.Engine.Id(repo.Id).Cols("JSON").Update(repo)

		if err != nil {
			beego.Trace("[Update Repository Error] " + namespace + "/" + repository)

			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository JSON data error.\"}"))
			this.StopRun()
		}

		beego.Trace("[Update Repository] " + namespace + "/" + repository)

	} else {
		_, err := models.Engine.Insert(repo)
		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Create the repository record error: \" + err.Error()}"))
			this.StopRun()
		}
		beego.Trace("[Insert Repository] " + namespace + "/" + repository)
	}

	beego.Trace("[Set Reponse HEADER]")

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Token", token)
	this.Ctx.Output.Context.ResponseWriter.Header().Set("WWW-Authenticate", token)
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

	this.Ctx.Output.Context.Output.SetStatus(200)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

func (this *RepositoryController) PutTag() {

	beego.Trace("Namespace: " + this.Ctx.Input.Param(":namespace"))
	beego.Trace("Repository: " + this.Ctx.Input.Param(":repo_name"))
	beego.Trace("Tag: " + this.Ctx.Input.Param(":tag"))
	beego.Trace("User-Agent: " + this.Ctx.Input.Header("User-Agent"))

	repository := &models.Repository{Namespace: this.Ctx.Input.Param(":namespace"), Repository: this.Ctx.Input.Param(":repo_name")}
	has, err := models.Engine.Get(repository)

	if has == false || err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Unknow namespace and repository.\"}"))
		this.StopRun()
	}

	tag := &models.Tag{Name: this.Ctx.Input.Param(":tag"), Repository: repository.Id}
	has, err = models.Engine.Get(tag)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search tag encounter error.\"}"))
		this.StopRun()
	}

	tag.Agent = this.Ctx.Input.Header("User-Agent")

	r, _ := regexp.Compile(`"([[:alnum:]]+)"`)
	imageIds := r.FindStringSubmatch(string(this.Ctx.Input.CopyBody()))

	tag.ImageId = imageIds[1]

	if has == true {
		_, err := models.Engine.Id(tag.Id).Update(tag)
		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the tag data error.\"}"))
			this.StopRun()
		}
	} else {
		_, err := models.Engine.Insert(tag)
		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Create the tag record error.\"}"))
			this.StopRun()
		}
	}

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")

	this.Ctx.Output.Context.Output.SetStatus(200)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

//根据最初上传的 Image 数据和每个 Image 的上传信息确定是否上传成功
func (this *RepositoryController) PutRepositoryImages() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Trace("[Namespace] " + namespace)
	beego.Trace("[Repository] " + repository)

	repo := &models.Repository{Namespace: namespace, Repository: repository}
	has, err := models.Engine.Get(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	var size int64

	if has == false {
		this.Ctx.Output.Context.Output.SetStatus(404)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()
	} else {
		//检查 Repository 的所有 Image Layer 是否都上传完成。
		var images []map[string]string
		uploaded := true
		checksumed := true

		json.Unmarshal([]byte(repo.JSON), &images)

		for _, i := range images {
			image := &models.Image{ImageId: i["id"]}
			has, err := models.Engine.Get(image)
			if err != nil {
				this.Ctx.Output.Context.Output.SetStatus(400)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search image error.\"}"))
				this.StopRun()
			}

			if has == false {
				this.Ctx.Output.Context.Output.SetStatus(404)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found image.\"}"))
				this.StopRun()
			} else {

				if image.Uploaded == false {
					uploaded = false
					break
				}

				if image.CheckSumed == false {
					checksumed = false
					break
				}

				size += image.Size
			}
		}

		if uploaded == false {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"The image layer upload not complete, please try again.\"}"))
			this.StopRun()
		}

		if checksumed == false {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"The image layer upload checksumed error, please try again.\"}"))
			this.StopRun()
		}
	}

	repo.Uploaded = true
	_, err = models.Engine.Id(repo.Id).Cols("Uploaded").Update(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository uploaded flag error, please try again.\"}"))
		this.StopRun()
	}

	repo.CheckSumed = true
	_, err = models.Engine.Id(repo.Id).Cols("CheckSumed").Update(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository checksumed flag error, please try again.\"}"))
		this.StopRun()
	}

	repo.Size = size
	_, err = models.Engine.Id(repo.Id).Cols("Size").Update(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository size error, please try again.\"}"))
		this.StopRun()
	}

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")

	this.Ctx.Output.Context.Output.SetStatus(204)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

func (this *RepositoryController) GetRepositoryImages() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Trace("[Namespace] " + namespace)
	beego.Trace("[Repository] " + repository)

	//查询 Repository 数据
	repo := &models.Repository{Namespace: namespace, Repository: repository, Uploaded: true, CheckSumed: true} //查询时要求已经是完成上传的 Repository
	has, err := models.Engine.Get(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	if has == false {
		this.Ctx.Output.Context.Output.SetStatus(404)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()
	} else {

		beego.Trace("[Repository] " + string(repo.Id))

		//存在 Repository 数据，查询所有的 Tag 数据。
		tags := make([]models.Tag, 0)
		err := models.Engine.Where("repository_id= ?", repo.Id).Find(&tags)

		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository tag error.\"}"))
			this.StopRun()
		}

		if len(tags) == 0 {
			this.Ctx.Output.Context.Output.SetStatus(404)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found any tag.\"}"))
			this.StopRun()
		}

		//根据 Tag 的 Image ID 值查询 ParentJSON 数据，然后同一在一个数组里面去重。
		var images []string
		for _, tag := range tags {
			image := &models.Image{ImageId: tag.ImageId}
			has, err := models.Engine.Get(image)

			if has == false || err != nil {
				this.Ctx.Output.Context.Output.SetStatus(400)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search image error.\"}"))
				this.StopRun()
			}

			if has == true {
				var parents []string

				beego.Trace(string(image.Id) + ":\n" + image.ParentJSON)

				if err := json.Unmarshal([]byte(image.ParentJSON), &parents); err != nil {
					this.Ctx.Output.Context.Output.SetStatus(400)
					this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Decode the parent image json data encouter error.\"}"))
					this.StopRun()
				}
				images = append(parents, images...)
			}
		}

		utils.RemoveDuplicateString(&images)

		//转换为 map 的对象返回
		var results []map[string]string
		for _, value := range images {
			result := make(map[string]string)
			result["id"] = value
			results = append(results, result)
		}

		imageIds, _ := json.Marshal(results)

		beego.Trace("Image ID:" + string(imageIds))

		//操作正常的输出
		this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
		this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

		this.Ctx.Output.Context.Output.SetStatus(200)
		this.Ctx.Output.Context.Output.Body(imageIds)

	}
}

func (this *RepositoryController) GetRepositoryTags() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Trace("[Namespace] " + namespace)
	beego.Trace("[Repository] " + repository)

	//查询 Repository 数据
	repo := &models.Repository{Namespace: namespace, Repository: repository, Uploaded: true, CheckSumed: true}
	has, err := models.Engine.Get(repo)
	if err != nil {
		this.Ctx.Output.Context.Output.SetStatus(400)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	if has == false {

		this.Ctx.Output.Context.Output.SetStatus(404)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()

	} else {

		beego.Trace("[Repository] " + string(repo.Id))

		//存在 Repository 数据，查询所有的 Tag 数据。
		tags := make([]models.Tag, 0)
		err := models.Engine.Where("repository_id= ?", repo.Id).Find(&tags)

		if err != nil {
			this.Ctx.Output.Context.Output.SetStatus(400)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository tag error.\"}"))
			this.StopRun()
		}

		if len(tags) == 0 {
			this.Ctx.Output.Context.Output.SetStatus(404)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found any tag.\"}"))
			this.StopRun()
		}

		results := make(map[string]string)
		for _, v := range tags {
			results[v.Name] = v.ImageId
		}

		result, _ := json.Marshal(results)

		beego.Trace("Tags: " + string(result))

		//操作正常的输出
		this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
		this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

		this.Ctx.Output.Context.Output.SetStatus(200)
		this.Ctx.Output.Context.Output.Body(result)
	}
}
