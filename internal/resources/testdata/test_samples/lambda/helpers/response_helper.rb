module ResponseHelper
  def self.success_response(data)
    {
      statusCode: 200,
      headers: {
        'Content-Type' => 'application/json',
        'Access-Control-Allow-Origin' => '*'
      },
      body: JSON.generate(data)
    }
  end

  def self.error_response(message, status_code = 400)
    {
      statusCode: status_code,
      headers: {
        'Content-Type' => 'application/json',
        'Access-Control-Allow-Origin' => '*'
      },
      body: JSON.generate({ error: message })
    }
  end
end