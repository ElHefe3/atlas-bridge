"""Bounded Atlas Bridge image update with durable, image-only rollback.

install.sh renders the @...@ values from library.env and installs this file
root-owned at LIBRARY_UPDATER. atlas-bridge-update.service runs `update` on a
timer; `recover` runs after every update attempt and at user-manager start.

No schema downgrade is promised: database migrations need a separate checkpoint.
The library backup holds the same lock, so updates and backups never overlap.
"""
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

LIBRARY_ROOT=Path('@LIBRARY_ROOT@')
ACCOUNT_HOME=Path('@LIBRARY_HOME@')
EXPECTED_UID='@LIBRARY_UID@'
EXPECTED_HOST='@LIBRARY_HOST@'
DEPLOY=LIBRARY_ROOT/'atlas-bridge/deploy'
LOCK=DEPLOY/'update.lock'
MARKER=DEPLOY/'lab-update-recovery.json'
REPORT=DEPLOY/'lab-update-result.json'
QUADLET=ACCOUNT_HOME/'.config/containers/systemd/atlas-bridge.container'
AUTH=ACCOUNT_HOME/'.config/containers/auth.json'
SERVICE='atlas-bridge.service'
REMOTE='ghcr.io/elhefe3/atlas-bridge:edge'
IMAGE=re.compile(r'(?:sha256:)?[a-f0-9]{64}')


def command(args,timeout=30):
    # Killing a timed-out pull must also terminate its descendants.
    p=subprocess.Popen(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
    try:out,err=p.communicate(timeout=timeout)
    except BaseException:
        try:os.killpg(p.pid,signal.SIGKILL)
        except ProcessLookupError:pass
        p.communicate()
        raise
    if p.returncode:raise RuntimeError('command-failed:'+Path(args[0]).name)
    return out.decode().strip()


def write(path,value):
    with tempfile.NamedTemporaryFile(mode='w',encoding='utf-8',dir=path.parent,delete=False) as f:
        temp=Path(f.name);os.fchmod(f.fileno(),0o600);json.dump(value,f);f.flush();os.fsync(f.fileno())
    os.replace(temp,path);sync()


def sync():
    fd=os.open(DEPLOY,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(fd)
    finally:os.close(fd)


@contextmanager
def lock():
    fd=os.open(LOCK,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'r+') as f:
        try:fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:raise RuntimeError('maintenance-busy')
        yield


def local_image():
    lines=[x.split('=',1)[1].strip() for x in QUADLET.read_text().splitlines() if x.startswith('Image=')]
    if len(lines)!=1 or not re.fullmatch(r'localhost/atlas-bridge:[A-Za-z0-9_.-]+',lines[0]):raise RuntimeError('unexpected-image-pin')
    return lines[0]


def image_id(name):
    value=command(['podman','image','inspect',name,'--format','{{.Id}}'])
    if not IMAGE.fullmatch(value):raise RuntimeError('invalid-image-id')
    return value.removeprefix('sha256:')


def healthy(expected,seconds=90):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        try:
            row=json.loads(command(['podman','inspect','atlas-bridge']))[0]
            if row['State'].get('Running') and row['Image'].removeprefix('sha256:')==expected and row['State'].get('Health',{}).get('Status')=='healthy':return
        except (RuntimeError,subprocess.TimeoutExpired):pass
        time.sleep(2)
    raise RuntimeError('atlas-health-deadline')


def recover():
    if not MARKER.exists():return False
    value=json.loads(MARKER.read_text())
    if set(value)!={'old_image','local_image','quadlet_sha256'} or not IMAGE.fullmatch(value['old_image']):raise RuntimeError('invalid-recovery-record')
    if local_image()!=value['local_image'] or hashlib.sha256(QUADLET.read_bytes()).hexdigest()!=value['quadlet_sha256']:raise RuntimeError('recovery-definition-drift')
    # Keep the record until both rollback commands and actual image health pass.
    command(['podman','tag',value['old_image'],value['local_image']])
    command(['systemctl','--user','reset-failed',SERVICE])
    command(['systemctl','--user','restart',SERVICE],timeout=110)
    healthy(value['old_image'])
    MARKER.unlink();sync();write(REPORT,{'status':'rolled-back','at_unix':time.time(),'image_id':value['old_image']})
    return True


def update():
    if MARKER.exists():recover()
    pin=local_image();old=image_id(pin);healthy(old,10)
    # Downloads never stop the current application. A slow pull is abandoned.
    command(['podman','pull','--authfile',str(AUTH),REMOTE],timeout=600)
    new=image_id(REMOTE)
    if new==old:
        write(REPORT,{'status':'current','at_unix':time.time(),'image_id':old});return
    write(MARKER,{'old_image':old,'local_image':pin,'quadlet_sha256':hashlib.sha256(QUADLET.read_bytes()).hexdigest()})
    try:
        command(['podman','tag',new,pin])
        command(['systemctl','--user','restart',SERVICE],timeout=110)
        healthy(new)
    except BaseException:
        recover();raise
    MARKER.unlink();sync();write(REPORT,{'status':'updated','at_unix':time.time(),'image_id':new})


def main():
    # Unrendered placeholders never match, so an uninstalled copy refuses to run.
    if str(os.getuid())!=EXPECTED_UID or os.uname().nodename!=EXPECTED_HOST or sys.argv[1:] not in (['update'],['recover']):raise SystemExit('wrong-identity-or-action')
    os.umask(0o077)
    signal.signal(signal.SIGTERM,lambda *_:sys.exit(143))
    try:
        with lock():
            if sys.argv[1]=='recover':recover()
            else:update()
    except RuntimeError as e:
        if str(e)=='maintenance-busy':
            print(json.dumps({'status':'skipped-maintenance-busy'}));return
        raise


if __name__=='__main__':main()
