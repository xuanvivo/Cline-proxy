package main

const loginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>登录 - Cline 代理管理面板</title>
<style>
:root{--bg:#0d1117;--bg2:#161b22;--bg3:#21262d;--border:#30363d;--text:#e6edf3;--text2:#8b949e;--accent:#58a6ff;--red:#f85149}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','Noto Sans',Helvetica,Arial,sans-serif;background:var(--bg);color:var(--text);font-size:14px;line-height:1.5;min-height:100vh;display:flex;align-items:center;justify-content:center}
.login-box{width:100%;max-width:360px;background:var(--bg2);border:1px solid var(--border);border-radius:8px;padding:32px;margin:16px}
.login-box h1{font-size:18px;text-align:center;margin-bottom:4px}
.login-box h1 span{color:var(--accent)}
.login-box .sub{text-align:center;color:var(--text2);font-size:12px;margin-bottom:24px}
.field{margin-bottom:16px}
.field label{display:block;font-size:12px;color:var(--text2);margin-bottom:6px}
input{width:100%;padding:9px 12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;font-family:inherit}
input:focus{outline:none;border-color:var(--accent)}
.btn{width:100%;padding:9px 14px;border:1px solid #1f6feb;border-radius:6px;background:#1f6feb;color:#fff;cursor:pointer;font-size:14px;transition:0.15s;display:flex;align-items:center;justify-content:center;gap:8px}
.btn:hover{background:#388bfd}
.btn:disabled{opacity:0.6;cursor:not-allowed}
.error{display:none;background:#3d1117;border:1px solid var(--red);color:var(--red);border-radius:6px;padding:8px 12px;font-size:13px;margin-bottom:16px}
.error.show{display:block}
.loading{display:inline-block;width:14px;height:14px;border:2px solid rgba(255,255,255,0.4);border-top-color:#fff;border-radius:50%;animation:spin 0.8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<div class="login-box">
  <h1>Cline <span>代理管理面板</span></h1>
  <div class="sub">请登录以继续</div>
  <div class="error" id="err"></div>
  <form id="form">
    <div class="field">
      <label>用户名</label>
      <input type="text" id="username" autocomplete="username" autofocus required>
    </div>
    <div class="field">
      <label>密码</label>
      <input type="password" id="password" autocomplete="current-password" required>
    </div>
    <button class="btn" type="submit" id="btn">登 录</button>
  </form>
</div>
<script>
const form=document.getElementById('form'),btn=document.getElementById('btn'),err=document.getElementById('err');
form.addEventListener('submit',async e=>{
  e.preventDefault();
  err.classList.remove('show');
  btn.disabled=true;
  btn.innerHTML='<span class="loading"></span> 登录中...';
  try{
    const res=await fetch('{{BASE}}/login',{
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body:JSON.stringify({username:document.getElementById('username').value,password:document.getElementById('password').value})
    });
    const data=await res.json().catch(()=>({}));
    if(res.ok&&data.success){location.href='{{BASE}}/';return}
    err.textContent=data.error||'登录失败';
    err.classList.add('show');
  }catch(ex){
    err.textContent='网络错误: '+ex.message;
    err.classList.add('show');
  }
  btn.disabled=false;
  btn.textContent='登 录';
});
</script>
</body>
</html>`
