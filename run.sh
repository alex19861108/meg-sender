./hey -m POST http://10.104.4.43:8000/facepp/v3/detect -d
./hey -m POST -d {"api_key":"ghHqRVYT6HhDlCD9AqGmqqJc2AWamYWJ", "api_secret":"AexkU80-VXgDXS6T0i0NZJxjNQZbBKWX"} http://10.104.4.23:12033/facepp/v3/detect
./hey -n 1 -c 1 -m POST -dataType DATA -D ./data/my.txt http://10.104.4.43:9090/facepp/detect
