package task

import (
	"os"
	"io"
	"log"
	"net"
	"fmt"
	"time"
	"strings"
	"strconv"
	"github.com/dzhenquan/filesync/model"
	"github.com/dzhenquan/filesync/util"
	"sync"
)

type FileTask struct {
	Status		int
	TaskID		string
	TaskInfo 	*TaskInfo
	Quit		chan bool
	mutex 		sync.Mutex
}


var (
	FileTasks []*FileTask
	taskMutex	sync.Mutex
)

func (fileTask *FileTask) NewFileTask() {

}

func (fileTask *FileTask) CreateFileTranServer() {
	listen, err := util.CreateSocketListen("", fileTask.TaskInfo.FilePort)
	if err != nil {
		log.Println("创建本地监听失败!")
		return
	}
	defer listen.Close()

	log.Println("File Listen ...")

	for {
		conn, err := listen.Accept()
		if err != nil {
			log.Fatal("接受新的连接请求失败,file err: ", err)
			continue
		}

		//log.Println("conn: ", conn.RemoteAddr())
		go fileTask.handleFileConn(conn)

		if fileTask.Status == util.TASK_IS_STOP {
			break
		}
	}
}

func (fileTask *FileTask) HandleTaskStartRequest()  {
	filelist, err := util.GetCurrentFileList(fileTask.TaskInfo.SrcPath)
	if err != nil {
		return
	}

	fileTotalCount := len(filelist)
	if fileTotalCount > util.MAX_TRAN_FILE_NUM {
		fileTotalPage := fileTotalCount / util.MAX_TRAN_FILE_NUM
		if fileTotalCount % util.MAX_TRAN_FILE_NUM > 0 {
			fileTotalPage++
		}

		for i := 0; i < fileTotalPage; i++ {
			var endFile int
			transFlag := make(chan bool, 1)

			startFile := i*util.MAX_TRAN_FILE_NUM
			curFileCount := fileTotalCount - startFile
			if curFileCount >= util.MAX_TRAN_FILE_NUM {
				endFile = startFile + util.MAX_TRAN_FILE_NUM
			} else {
				endFile = startFile + curFileCount
			}

			if fileTask.Status == util.TASK_IS_STOP {
				log.Println("开始退出文件传输....... ", fileTask.Status)
				close(transFlag)
				return
			}

			fileTask.handleMaxFileTransNums(transFlag, filelist[startFile:endFile])

			<-transFlag
			close(transFlag)
		}
	} else {
		transFlag := make(chan bool, 1)

		if fileTask.Status == util.TASK_IS_STOP {
			log.Println("开始退出文件传输,....... ", fileTask.Status)
			close(transFlag)
			return
		}

		fileTask.handleMaxFileTransNums(transFlag, filelist[:fileTotalCount])
		<-transFlag
		close(transFlag)
	}

	fileTask.handleTaskFinishUpdateStatusTime()
	return
}

// handle file info and recv file
func (fileTask *FileTask) handleFileConn(conn net.Conn) {
	for {
		buf := make([]byte,util.MAX_MESSAGE_LEN)

		recvSize, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Println("读取文件属性信息失败!")
			continue
		}
		defer conn.Close()

		fileSlice := strings.Split(string(buf[:recvSize]), "+")

		filename := fileTask.TaskInfo.DestPath + "/" + fileSlice[0]
		filesize, _ := strconv.Atoi(fileSlice[1])

		conn.Write([]byte("ok"))
		startTime := time.Now().Format("2006-01-02 15:04:05")
		tFileLog := &model.TaskFileLog{
			TaskID:fileTask.TaskID,
			SrcHost:fileTask.TaskInfo.SrcHost,
			DestHost:fileTask.TaskInfo.DestHost,
			FileName: fileSlice[0],
			FileSize: fileSlice[1],
			FileStartTime: startTime,
		}

		var transResult string
		if _, err := util.RecvFile(conn, filename, uint64(filesize)); err == nil {
			log.Printf("文件[%s]接收完毕,TaskID:[%s]!\n", filename,fileTask.TaskID)

			transResult = "文件接收成功"
			if fileTask.TaskInfo.TranType == util.FILE_CUT{
				conn.Write([]byte("ok"))
			}
			//hash, _ := HashFile(filename)
			//fmt.Println("md5:", hash)
		} else {
			transResult = "文件接收失败"
			log.Printf("文件[%s]接收失败,TaskID:[%s]!\n", filename,fileTask.TaskID)
		}
		endTime := time.Now().Format("2006-01-02 15:04:05")
		tFileLog.FileEndTime = endTime
		tFileLog.TransResult = transResult
		tFileLog.Insert()
	}
}

func (fileTask *FileTask) handleMaxFileTransNums(transFlag chan<- bool, filelist []string) {
	fileThreadCount := len(filelist)

	flag := make(chan bool, fileThreadCount)
	defer close(flag)
	
	for i := 0; i < fileThreadCount; i++ {
		go fileTask.handleFileTrans(filelist[i], flag)

		if fileTask.Status == util.TASK_IS_STOP {
			log.Println("开始退出文件传输,....... ", fileTask.Status)
			transFlag<-true
			return
		}
	}

	for i := 0; i < fileThreadCount; i++ {
		<-flag
	}
	transFlag<-true
}

func (fileTask *FileTask) handleFileTrans(filename string, flag chan<- bool) {

	fileInfo, err := os.Stat(filename)
	if err != nil {
		fmt.Printf("File [%s] is not exists!\n", filename)
		return
	}

	conn, err := util.CreateSocketConnect(fileTask.TaskInfo.DestHost, fileTask.TaskInfo.FilePort)
	if err != nil {
		return
	}
	defer conn.Close()

	fileStr := fmt.Sprintf("%s+%d", fileInfo.Name(), fileInfo.Size())
	_, err = conn.Write([]byte(fileStr))
	if err != nil {
		fmt.Println("Send Failure!")
		return
	}

	buf := make([]byte, util.MAX_MESSAGE_LEN)
	recvSize, err := conn.Read(buf)
	if err != nil {
		flag<-true
		return
	}

	recvOk := buf[:recvSize]
	if strings.Compare(string(recvOk), "ok") == 0 {
		startTime := time.Now().Format("2006-01-02 15:04:05")
		tFileLog := &model.TaskFileLog{
			TaskID: fileTask.TaskID,
			SrcHost:fileTask.TaskInfo.SrcHost,
			DestHost:fileTask.TaskInfo.DestHost,
			FileName: fileInfo.Name(),
			FileSize: strconv.Itoa(int(fileInfo.Size())),
			FileStartTime: startTime,
		}
		var transResult string
		if sendSize, err := util.SendFile(conn, filename); err != nil {
			log.Printf("文件[%s]发送失败,TaskID:[%s],Len:[%d]!\n",
				fileInfo.Name(), fileTask.TaskID ,sendSize)
			transResult = "文件发送失败"
		} else {
			log.Printf("文件[%s]发送完毕,TaskID:[%s],Len:[%d]!\n",
				fileInfo.Name(), fileTask.TaskID, sendSize)
			transResult = "文件发送成功"
		}
		endTime := time.Now().Format("2006-01-02 15:04:05")
		tFileLog.FileEndTime = endTime
		tFileLog.TransResult = transResult
		tFileLog.Insert()

		if fileTask.TaskInfo.TranType == util.FILE_CUT {
			bufDelete := make([]byte, util.MAX_MESSAGE_LEN)
			recvSize, err = conn.Read(bufDelete)
			if err != nil {
				flag<-true
				return
			}
			recvDelete := bufDelete[:recvSize]
			if strings.Compare(string(recvDelete), "ok") == 0 {
				os.Remove(filename)
			}
		}
	}
	flag<-true
}

func (fileTask *FileTask) handleTaskFinishUpdateStatusTime() error {

	time.Sleep(5*time.Millisecond)

	nowTime := time.Now().Unix()

	fileTask.SetFileTaskStatus(util.TASK_IS_RUNED)

	// 更新数据库中任务状态
	tFileInfo := model.TaskFileInfo{
		TaskID:fileTask.TaskID,
		Status:fileTask.Status,
		LastFinishTime:nowTime,
	}

	return tFileInfo.UpdateTaskStatusTime()
}

func (fileTask *FileTask) SetFileTaskStatus(status int) {
	fileTask.mutex.Lock()
	defer fileTask.mutex.Unlock()

	fileTask.Status = status
}

func FindFileTaskByTaskIDFromList(taskID string) (*FileTask) {
	taskMutex.Lock()
	defer taskMutex.Unlock()

	if len(taskID) == 0 {
		return nil
	}

	/*从任务链表中查找该任务*/
	for _, fileTask := range FileTasks {
		if strings.Compare(fileTask.TaskID, taskID) == 0 {
			return fileTask
		}
	}
	return nil
}

func RemoveFileTaskFromList(slice []*FileTask, elems ...*FileTask) []*FileTask {
	taskMutex.Lock()
	defer taskMutex.Unlock()

	isInElems := make(map[*FileTask]bool)
	for _, elem := range elems {
		isInElems[elem] = true
	}
	w := 0
	for _, elem := range slice {
		if !isInElems[elem] {
			slice[w] = elem
			w += 1
		}
	}
	return slice[:w]
}