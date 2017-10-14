build: isutomo/isutomo_deploy isuwitter/isuwitter_deploy

isutomo/isutomo_deploy: isutomo/app.go
	cd isutomo && glide install && GOOS=linux go build -o isutomo_deploy app.go

isuwitter/isuwitter_deploy: isuwitter/app.go
	cd isuwitter && glide install && GOOS=linux go build -o isuwitter_deploy app.go

clean:
	rm isutomo/isutomo_deploy
	rm isutomo/isutomo
	rm isuwitter/isuwitter_deploy
	rm isuwitter/isuwitter

deploy: build
	echo "start deploy by $(USER)" | notify_slack
	ssh -t isu sudo systemctl stop isucon-go-isutomo.service
	scp isutomo/isutomo_deploy isu:/var/www/webapp/go/isutomo/isutomo
	ssh -t isu sudo systemctl start isucon-go-isutomo.service

	ssh -t isu sudo systemctl stop isucon-go-isuwitter.service
	scp isuwitter/isuwitter_deploy isu:/var/www/webapp/go/isuwitter/isuwitter
	ssh -t isu sudo systemctl start isucon-go-isuwitter.service

	rsync -av isuwitter/public/ isu:/var/www/webapp/go/isuwitter/public/
	rsync -av isuwitter/views/ isu:/var/www/webapp/go/isuwitter/views/
	echo "finish deploy by $(USER)" | notify_slack

.PHONY:
	build develop clean
