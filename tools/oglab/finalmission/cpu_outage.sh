cd /root
mkdir -p /root/cpuo; rm -f /root/cpuo/*
echo "healthy window: $(date +%T)" > /root/cpuo/log.txt
bash /root/cpuproc.sh 600 > /root/cpuo/healthy.txt
systemctl stop stayconnect-acctd; echo "acctd stopped $(date +%T)" >> /root/cpuo/log.txt
sleep 180
echo "outage window: $(date +%T)" >> /root/cpuo/log.txt
bash /root/cpuproc.sh 600 > /root/cpuo/outage.txt
echo "outage window end $(date +%T) set=$(nft list set inet stayconnect phase3_auth_ipv4 | grep -c expires)" >> /root/cpuo/log.txt
systemctl start stayconnect-acctd; sleep 60
echo "recovery window: $(date +%T)" >> /root/cpuo/log.txt
bash /root/cpuproc.sh 120 > /root/cpuo/after.txt
echo done >> /root/cpuo/log.txt
