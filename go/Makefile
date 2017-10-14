build: isutomo/isutomo isuwitter/isuwitter

isutomo/isutomo: isutomo/app.go
	cd isutomo && glide install && GOOS=linux go build -o isutomo app.go

isuwitter/isuwitter: isuwitter/app.go
	cd isuwitter && glide install && GOOS=linux go build -o isuwitter app.go

clean:
	rm isutomo/isutomo
	rm isuwitter/isuwitter

deploy: build
	echo "start deploy by $(USER)" | notify_slack
	ssh -t isu sudo systemctl stop isucon-go-isutomo.service
	scp isutomo/isutomo isu:/var/www/webapp/go/isutomo/
	ssh -t isu sudo systemctl start isucon-go-isutomo.service

	ssh -t isu sudo systemctl stop isucon-go-isuwitter.service
	scp isuwitter/isuwitter isu:/var/www/webapp/go/isuwitter/
	ssh -t isu sudo systemctl start isucon-go-isuwitter.service

	rsync -av isuwitter/public/ isu:/var/www/webapp/go/isuwitter/public/
	rsync -av isuwitter/views/ isu:/var/www/webapp/go/isuwitter/views/
	echo "finish deploy by $(USER)" | notify_slack

.PHONY:
	build develop
